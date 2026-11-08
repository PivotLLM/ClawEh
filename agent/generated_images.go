// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/h2non/filetype"

	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/fileutil"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
	"github.com/PivotLLM/ClawEh/utils"
)

// generatedImagesDir is where a model's generated images are saved, under a
// folder per day, relative to the agent's workspace (a clone's is its
// source's, as for its file tools).
const generatedImagesDir = "files/generated"

// imageResult is what became of one generated image.
type imageResult struct {
	// reply is the line the turn's reply carries about the image; empty when
	// the image itself reached the chat.
	reply string
	// note is what the model is told about it, kept in its conversation.
	note string
}

// handleImages saves and delivers the images of one model response, in
// order: each image with data is saved in the agent's workspace and, when
// the turn answers a chat that can receive files, sent there through the
// msg_send_file path (SendFiles); an image given as a URL is never fetched,
// only named in the reply. The results are kept on the turn for its reply
// and its stored messages.
func (t *llmTurn) handleImages(ctx context.Context, images []providers.GeneratedImage) {
	for _, img := range images {
		r := t.handleImage(ctx, img)
		t.imageCount++
		t.iterationNotes = append(t.iterationNotes, r)
		if r.reply != "" {
			t.imageReplies = append(t.imageReplies, r.reply)
		}
	}
}

// handleImage handles one generated image and logs what became of it.
func (t *llmTurn) handleImage(ctx context.Context, img providers.GeneratedImage) imageResult {
	fields := map[string]any{"agent_id": t.agent.ID, "agent": t.agent.Label(), "model": t.model, "size": img.Size}
	if len(img.Data) == 0 {
		if strings.HasPrefix(img.URL, "data:") {
			// Data the provider could not decode: nothing to save, and the
			// raw text is no use in a chat.
			fields["outcome"] = "not decodable"
			logger.InfoCF("agent", "Generated image", fields)
			line := "An image could not be read."
			return imageResult{reply: line, note: "[" + line + "]"}
		}
		fields["outcome"] = "url"
		fields["url"] = img.URL
		logger.InfoCF("agent", "Generated image", fields)
		line := "Image: " + img.URL
		return imageResult{reply: line, note: line}
	}

	path, err := t.saveImage(img)
	if err != nil {
		fields["outcome"] = "not saved"
		fields["error"] = err.Error()
		logger.WarnCF("agent", "Generated image", fields)
		line := "An image could not be saved."
		return imageResult{reply: line, note: "[" + line + "]"}
	}
	fields["path"] = path

	outcome, r := t.deliverImage(ctx, img, path)
	fields["outcome"] = outcome
	logger.InfoCF("agent", "Generated image", fields)
	return r
}

// deliverImage sends a saved image to the turn's chat when it can go there,
// returning the outcome for the log and the result.
func (t *llmTurn) deliverImage(ctx context.Context, img providers.GeneratedImage, path string) (string, imageResult) {
	saved := "Image saved as " + path + "."
	channel, chatID := t.opts.Channel, t.opts.ChatID
	switch {
	case constants.RepliesToAgent(channel):
		return "saved", imageResult{reply: saved, note: saved}
	case channel == "" || chatID == "" || constants.IsInternalChannel(channel):
		line := "This chat can't receive files. " + saved
		return "saved, chat can't receive files", imageResult{reply: line, note: line}
	}
	maxSize := int64(t.al.GetConfig().Agents.Defaults.GetMaxMediaSize())
	if img.Size > maxSize {
		line := "Image too large to send. " + saved
		return "saved, too large to send", imageResult{reply: line, note: line}
	}

	f, err := os.Open(filepath.Join(t.agent.Workspace, filepath.FromSlash(path)))
	if err != nil {
		line := "Image not sent: it could not be read. " + saved
		return "saved, not sent: " + err.Error(), imageResult{reply: line, note: line}
	}
	defer utils.CloseQuietly(f)
	results, err := t.al.SendFiles(ctx, channel, chatID,
		[]tools.OutgoingFile{{File: f, Filename: filepath.Base(path), MaxSize: maxSize}})
	switch {
	case errors.Is(err, tools.ErrNoFileSupport):
		line := "This chat can't receive files. " + saved
		return "saved, chat can't receive files", imageResult{reply: line, note: line}
	case err == nil && len(results) == 1 && results[0] == nil:
		return "sent", imageResult{note: "[Image sent to the chat and saved as " + path + ".]"}
	case err == nil && len(results) == 1 && errors.Is(results[0], tools.ErrDeliveryUnconfirmed):
		return "sending, not confirmed", imageResult{note: "[Image sent to the chat (not confirmed yet) and saved as " + path + ".]"}
	case err == nil && len(results) == 1:
		err = results[0]
	case err == nil:
		err = errors.New("no delivery report")
	}
	line := fmt.Sprintf("Image not sent (%v). %s", err, saved)
	return "saved, not sent: " + err.Error(), imageResult{reply: line, note: line}
}

// saveImage writes img's data in the agent's workspace, under a folder for
// the day, with the area's exact modes; it returns the workspace path.
func (t *llmTurn) saveImage(img providers.GeneratedImage) (string, error) {
	now := t.al.clk().Now()
	name := fmt.Sprintf("image-%s%03d-%s%s", now.Format("150405"), now.Nanosecond()/1e6,
		uuid.NewString()[:8], imageExtension(img))
	rel := generatedImagesDir + "/" + now.Format("2006-01-02") + "/" + name
	full := filepath.Join(t.agent.Workspace, filepath.FromSlash(rel))
	modes := agentModes(t.al.GetConfig(), full)
	if err := fileutil.MkdirAllExact(filepath.Dir(full), modes.Dir); err != nil {
		return "", err
	}
	if err := fileutil.WriteFileExact(full, img.Data, modes.File); err != nil {
		return "", err
	}
	return rel, nil
}

// imageExtension is the file extension for an image: from its content type,
// else from its bytes, else ".bin".
func imageExtension(img providers.GeneratedImage) string {
	ct, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(img.ContentType)), ";")
	switch ct {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/svg+xml":
		return ".svg"
	}
	if kind, err := filetype.Match(img.Data); err == nil && kind != filetype.Unknown {
		return "." + kind.Extension
	}
	return ".bin"
}

// withImageNotes is content followed by lines, one per line, after a blank
// line.
func withImageNotes(content string, lines []string) string {
	if len(lines) == 0 {
		return content
	}
	joined := strings.Join(lines, "\n")
	if content == "" {
		return joined
	}
	return content + "\n\n" + joined
}

// notesFor is the model notes of results.
func notesFor(results []imageResult) []string {
	notes := make([]string, 0, len(results))
	for _, r := range results {
		notes = append(notes, r.note)
	}
	return notes
}

// sentNotesFor is the model notes of the images among results that reached
// the chat, which the reply does not mention.
func sentNotesFor(results []imageResult) []string {
	var notes []string
	for _, r := range results {
		if r.reply == "" {
			notes = append(notes, r.note)
		}
	}
	return notes
}
