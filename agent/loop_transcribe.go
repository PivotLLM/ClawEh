// ClawEh - Personal AI Assistant
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/utils"
)

var audioAnnotationRe = regexp.MustCompile(`\[(voice|audio)(?::[^\]]*)?\]`)

// transcribeAudioInMessage resolves audio media refs, transcribes them, and
// replaces audio annotations in msg.Content with the transcribed text.
// Returns the (possibly modified) message and true if audio was transcribed.
func (al *AgentLoop) transcribeAudioInMessage(ctx context.Context, msg bus.InboundMessage) (bus.InboundMessage, bool) {
	if al.transcriber == nil || al.mediaStore == nil || len(msg.Media) == 0 {
		return msg, false
	}

	// Transcribe each audio media ref in order. Non-audio refs (and audio we can't
	// resolve) pass through in kept for normal attachment handling; transcribed
	// audio is dropped from Media so it isn't also materialized as a raw file the
	// model can't use (it becomes a "[voice: ...]" transcript in the content).
	var transcriptions []string
	kept := make([]string, 0, len(msg.Media))
	for _, ref := range msg.Media {
		path, meta, err := al.mediaStore.ResolveWithMeta(ref)
		if err != nil {
			logger.WarnCF("voice", "Failed to resolve media ref", map[string]any{"ref": ref, "error": err})
			kept = append(kept, ref)
			continue
		}
		if !utils.IsAudioFile(meta.Filename, meta.ContentType) {
			kept = append(kept, ref)
			continue
		}
		result, err := al.transcriber.Transcribe(ctx, path)
		if err != nil {
			logger.WarnCF("voice", "Transcription failed", map[string]any{"ref": ref, "error": err})
			transcriptions = append(transcriptions, "")
			continue
		}
		transcriptions = append(transcriptions, result.Text)
	}

	if len(transcriptions) == 0 {
		return msg, false
	}
	msg.Media = kept

	al.sendTranscriptionFeedback(ctx, msg.Channel, msg.ChatID, msg.MessageID, transcriptions)

	// Replace audio annotations sequentially with transcriptions.
	idx := 0
	newContent := audioAnnotationRe.ReplaceAllStringFunc(msg.Content, func(match string) string {
		if idx >= len(transcriptions) {
			return match
		}
		text := transcriptions[idx]
		idx++
		return "[voice: " + text + "]"
	})

	// Append any remaining transcriptions not matched by an annotation.
	for ; idx < len(transcriptions); idx++ {
		newContent += "\n[voice: " + transcriptions[idx] + "]"
	}

	msg.Content = newContent
	return msg, true
}

var receivedFileNameUnsafeRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// materializeInboundMedia copies any media attached to an inbound message into a
// "tmp" folder inside the agent's workspace, so the agent's (workspace-sandboxed)
// file tools can actually read what the user sent. Returns a note to append to the
// user message naming the workspace-relative paths, or "" if nothing was written.
// The MediaStore copies still exist (with their own TTL cleanup); these workspace
// copies are intentionally left for the agent to use.
func (al *AgentLoop) materializeInboundMedia(msg bus.InboundMessage, agent *AgentInstance) string {
	if al.mediaStore == nil || len(msg.Media) == 0 || agent == nil || agent.Workspace == "" {
		return ""
	}
	tmpDir := filepath.Join(agent.Workspace, "tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		logger.WarnCF("agent", "received media: create tmp dir failed", map[string]any{"error": err.Error()})
		return ""
	}
	// Bound growth: drop attachments from earlier turns before adding this turn's.
	sweepReceivedMedia(tmpDir, time.Now())
	var rels []string
	for _, ref := range msg.Media {
		src, meta, err := al.mediaStore.ResolveWithMeta(ref)
		if err != nil {
			continue
		}
		name := receivedFileName(ref, meta.Filename)
		if err := copyFileContents(src, filepath.Join(tmpDir, name)); err != nil {
			logger.WarnCF("agent", "received media: copy failed", map[string]any{"ref": ref, "error": err.Error()})
			continue
		}
		rels = append(rels, "tmp/"+name)
	}
	if len(rels) == 0 {
		return ""
	}
	return "\n\n[Received attachment(s) saved in your workspace: " + strings.Join(rels, ", ") + "]"
}

// receivedMediaTTL bounds how long materialized inbound attachments linger in
// <workspace>/tmp. They are swept on the next materialize for that agent.
const receivedMediaTTL = 24 * time.Hour

// sweepReceivedMedia removes files in dir older than receivedMediaTTL so the
// received-media tmp dir does not grow without bound. Best-effort; errors ignored.
func sweepReceivedMedia(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > receivedMediaTTL {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// receivedFileName builds a collision-resistant, sandbox-safe filename from a media
// ref ("media://<uuid>") and its original filename.
func receivedFileName(ref, filename string) string {
	id := strings.TrimPrefix(ref, "media://")
	if i := strings.IndexByte(id, '-'); i > 0 {
		id = id[:i] // first uuid segment is enough to disambiguate
	}
	base := filepath.Base(strings.TrimSpace(filename))
	base = receivedFileNameUnsafeRe.ReplaceAllString(base, "_")
	base = strings.Trim(base, "._")
	if base == "" {
		base = "file"
	}
	if id == "" {
		return base
	}
	return id + "_" + base
}

// copyFileContents streams src to dst, creating/truncating dst.
func copyFileContents(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// sendTranscriptionFeedback sends feedback to the user with the result of
// audio transcription if the option is enabled. It uses Manager.SendMessage
// which executes synchronously (rate limiting, splitting, retry) so that
// ordering with the subsequent placeholder is guaranteed.
func (al *AgentLoop) sendTranscriptionFeedback(
	ctx context.Context,
	channel, chatID, messageID string,
	validTexts []string,
) {
	if !al.cfg.Voice.EchoTranscription {
		return
	}
	if al.channelManager == nil {
		return
	}

	var nonEmpty []string
	for _, t := range validTexts {
		if t != "" {
			nonEmpty = append(nonEmpty, t)
		}
	}

	var feedbackMsg string
	if len(nonEmpty) > 0 {
		feedbackMsg = "Transcript: " + strings.Join(nonEmpty, "\n")
	} else {
		feedbackMsg = "No voice detected in the audio"
	}

	err := al.channelManager.SendMessage(ctx, bus.OutboundMessage{
		Channel:          channel,
		ChatID:           chatID,
		Content:          feedbackMsg,
		ReplyToMessageID: messageID,
	})
	if err != nil {
		logger.WarnCF("voice", "Failed to send transcription feedback", map[string]any{"error": err.Error()})
	}
}

// inferMediaType determines the media type ("image", "audio", "video", "file")
// from a filename and MIME content type.
func inferMediaType(filename, contentType string) string {
	ct := strings.ToLower(contentType)
	fn := strings.ToLower(filename)

	if strings.HasPrefix(ct, "image/") {
		return "image"
	}
	if strings.HasPrefix(ct, "audio/") || ct == "application/ogg" {
		return "audio"
	}
	if strings.HasPrefix(ct, "video/") {
		return "video"
	}

	// Fallback: infer from extension
	ext := filepath.Ext(fn)
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg":
		return "image"
	case ".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac", ".wma", ".opus":
		return "audio"
	case ".mp4", ".avi", ".mov", ".webm", ".mkv":
		return "video"
	}

	return "file"
}
