// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/h2non/filetype"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/fileutil"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/media"
	"github.com/PivotLLM/ClawEh/tools"
	"github.com/PivotLLM/ClawEh/utils"
)

// sendFilesTimeout bounds how long msg_send_file waits for the channel to
// report its files delivered: long enough for an upload of the largest file
// allowed and the manager's retries, short enough not to hold the turn.
const sendFilesTimeout = time.Minute

// fileChannels says which channels can send files (channels.Manager).
type fileChannels interface {
	SupportsMedia(channel string) bool
}

var _ tools.FileSender = (*AgentLoop)(nil)

// SendFiles implements tools.FileSender: each file is copied to the media
// staging folder (so the channel sends what was checked, and the agent's
// own file is never handed to the media store, which deletes what it
// holds), published to the chat as its own message, and waited for until
// the channel reports it or sendFilesTimeout passes. A staged copy is
// removed once its delivery is reported.
func (al *AgentLoop) SendFiles(ctx context.Context, channel, chatID string, files []tools.OutgoingFile) ([]error, error) {
	if al.fileChannels == nil || !al.fileChannels.SupportsMedia(channel) {
		return nil, tools.ErrNoFileSupport
	}
	store := al.mediaStore
	if store == nil {
		return nil, errors.New("no media store")
	}

	waitCtx, cancel := al.clk().WithTimeout(ctx, sendFilesTimeout)
	defer cancel()

	results := make([]error, len(files))
	outcomes := make([]chan error, len(files))
	for i, f := range files {
		outcomes[i] = make(chan error, 1)
		if err := al.publishFile(waitCtx, store, channel, chatID, f, outcomes[i]); err != nil {
			results[i] = err
			outcomes[i] = nil
		}
	}
	for i, outcome := range outcomes {
		if outcome == nil {
			continue
		}
		select {
		case results[i] = <-outcome:
		case <-waitCtx.Done():
			results[i] = tools.ErrDeliveryUnconfirmed
		}
	}

	sent := 0
	for _, err := range results {
		if err == nil {
			sent++
		}
	}
	logger.InfoCF("agent", "Files sent to chat", map[string]any{
		"channel": channel, "chat_id": chatID, "files": len(files), "sent": sent,
	})
	return results, nil
}

// publishFile stages f and publishes it to the chat; outcome receives the
// channel's report.
func (al *AgentLoop) publishFile(ctx context.Context, store media.MediaStore, channel, chatID string, f tools.OutgoingFile, outcome chan<- error) error {
	staged, err := stageFile(f.File, f.Filename, f.MaxSize)
	if errors.Is(err, errTooLarge) {
		return fmt.Errorf("it is more than the limit of %d bytes", f.MaxSize)
	}
	if err != nil {
		logger.WarnCF("agent", "File not staged for sending", map[string]any{"file": f.Filename, "error": err.Error()})
		return errors.New("could not be read")
	}
	contentType := detectMediaType(staged)
	scope := "tool:send_file:" + uuid.NewString()
	ref, err := store.Store(staged, media.MediaMeta{Filename: f.Filename, ContentType: contentType, Source: "tool:send_file"}, scope)
	if err != nil {
		removeStaged(staged)
		return fmt.Errorf("could not be prepared: %w", err)
	}
	release := func() {
		if err := store.ReleaseAll(scope); err != nil {
			logger.WarnCF("agent", "Sent file's staged copy not removed", map[string]any{"file": f.Filename, "error": err.Error()})
		}
	}
	msg := bus.OutboundMediaMessage{
		Channel: channel,
		ChatID:  chatID,
		Parts: []bus.MediaPart{{
			Type:        sendFileType(contentType),
			Ref:         ref,
			Filename:    f.Filename,
			ContentType: contentType,
		}},
		OnDelivery: func(err error) {
			// Never blocks: the buffer holds the one report SendFiles reads.
			select {
			case outcome <- err:
			default:
			}
			go release()
		},
	}
	pubCtx, pubCancel := context.WithTimeout(ctx, publishTimeout)
	defer pubCancel()
	if err := al.bus.PublishOutboundMedia(pubCtx, msg); err != nil {
		release()
		return fmt.Errorf("could not be queued: %w", err)
	}
	return nil
}

// sendFileType is the media part type of a file sent to a chat: an image in
// a format chats show as a picture is an image, everything else a file.
func sendFileType(contentType string) string {
	switch contentType {
	case "image/jpeg", "image/png", "image/webp":
		return "image"
	}
	return "file"
}

// stagedPrefix starts the name of every staged copy in the media staging
// folder, so copies a stop left behind are found at the next start
// (RemoveStagedFileCopies).
const stagedPrefix = "send-"

// errTooLarge is a file that grew past its limit after it was checked.
var errTooLarge = errors.New("file larger than its limit")

// stageFile copies at most maxSize bytes of src into the media staging
// folder and returns the copy's path; a src with more is refused
// (errTooLarge).
func stageFile(src io.Reader, filename string, maxSize int64) (string, error) {
	dir := utils.MediaTempDir()
	dirMode, fileMode := utils.MediaModes()
	if err := fileutil.MkdirAllExact(dir, dirMode); err != nil {
		return "", err
	}
	dst, err := os.CreateTemp(dir, stagedPrefix+"*-"+utils.SanitizeFilename(filepath.Base(filename)))
	if err != nil {
		return "", err
	}
	n, err := io.Copy(dst, io.LimitReader(src, maxSize+1))
	if err == nil && n > maxSize {
		err = errTooLarge
	}
	if err == nil {
		err = dst.Chmod(fileMode)
	}
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		removeStaged(dst.Name())
		return "", err
	}
	return dst.Name(), nil
}

// RemoveStagedFileCopies removes the staged copies of sent files a previous
// run left in the media staging folder (a stop before their delivery was
// reported). The gateway calls it once at start, after the staging folder is
// set.
func RemoveStagedFileCopies() {
	dir := utils.MediaTempDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.WarnCF("agent", "Staged file copies not checked", map[string]any{"dir": dir, "error": err.Error()})
		}
		return
	}
	removed := 0
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasPrefix(e.Name(), stagedPrefix) {
			removeStaged(filepath.Join(dir, e.Name()))
			removed++
		}
	}
	if removed > 0 {
		logger.InfoCF("agent", "Removed staged copies of files left by an earlier run", map[string]any{"count": removed})
	}
}

// detectMediaType determines the MIME type of a file: magic bytes
// (h2non/filetype) first, then the extension.
func detectMediaType(path string) string {
	kind, err := filetype.MatchFile(path)
	if err == nil && kind != filetype.Unknown {
		return kind.MIME.Value
	}
	if ext := filepath.Ext(path); ext != "" {
		if t := mime.TypeByExtension(ext); t != "" {
			return t
		}
	}
	return "application/octet-stream"
}

// removeStaged removes a staged copy that will not be sent.
func removeStaged(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		logger.WarnCF("agent", "Staged file not removed", map[string]any{"path": path, "error": err.Error()})
	}
}
