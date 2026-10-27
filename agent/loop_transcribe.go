// ClawEh
// License: MIT

package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/utils"
)

var audioAnnotationRe = regexp.MustCompile(`\[(voice|audio)(?::[^\]]*)?\]`)

// untranscribedVoiceNote is what the model sees in place of a voice message
// whose transcription failed, so it can tell the user instead of answering
// an empty message.
const untranscribedVoiceNote = "[voice message: could not be transcribed, so its content is unknown; let the user know]"

// voiceTranscript is the outcome of transcribing one audio attachment.
type voiceTranscript struct {
	text   string
	failed bool
}

// annotation is what replaces the audio annotation in the user message.
func (v voiceTranscript) annotation() string {
	if v.failed {
		return untranscribedVoiceNote
	}
	return "[voice: " + v.text + "]"
}

// transcribeAudioInMessage resolves audio media refs, transcribes them, and
// replaces audio annotations in msg.Content with the transcribed text, or
// with untranscribedVoiceNote for audio that could not be transcribed.
// target is the agent the message is addressed to (nil if unknown); it names
// the agent in the echoed feedback. Returns the (possibly modified) message
// and true if the message carried audio.
func (al *AgentLoop) transcribeAudioInMessage(ctx context.Context, msg bus.InboundMessage, target *AgentInstance) (bus.InboundMessage, bool) {
	if al.transcriber == nil || al.mediaStore == nil || len(msg.Media) == 0 {
		return msg, false
	}

	// Transcribe each audio media ref in order. Non-audio refs (and audio we can't
	// resolve) pass through in kept for normal attachment handling; audio is
	// dropped from Media so it isn't also materialized as a raw file the model
	// can't use (it becomes a "[voice: ...]" transcript, or the untranscribed
	// note, in the content).
	var transcriptions []voiceTranscript
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
			logger.WarnCF("voice", "Transcription failed", map[string]any{
				"ref": ref, "channel": msg.Channel, "chat_id": msg.ChatID, "error": err.Error(),
			})
			al.alertUntranscribed(msg.Channel, err)
			transcriptions = append(transcriptions, voiceTranscript{failed: true})
			continue
		}
		transcriptions = append(transcriptions, voiceTranscript{text: result.Text})
	}

	if len(transcriptions) == 0 {
		return msg, false
	}
	msg.Media = kept

	al.sendTranscriptionFeedback(ctx, msg.Channel, msg.ChatID, msg.MessageID, transcriptionFeedback(target, transcriptions))

	// Replace audio annotations sequentially with transcriptions.
	idx := 0
	newContent := audioAnnotationRe.ReplaceAllStringFunc(msg.Content, func(match string) string {
		if idx >= len(transcriptions) {
			return match
		}
		text := transcriptions[idx].annotation()
		idx++
		return text
	})

	// Append any remaining transcriptions not matched by an annotation.
	var newContentSb78 strings.Builder
	for ; idx < len(transcriptions); idx++ {
		newContentSb78.WriteString("\n" + transcriptions[idx].annotation())
	}
	newContent += newContentSb78.String()

	msg.Content = newContent
	return msg, true
}

// alertUntranscribed raises an urgent alert, keyed by channel, for a voice
// message that every speech backend failed to transcribe.
func (al *AgentLoop) alertUntranscribed(channel string, err error) {
	details := []rune(strings.TrimSpace(err.Error()))
	if len(details) > 200 {
		details = details[:200]
	}
	al.Alerter().Send(alerter.Alert{
		Priority:    alerter.Urgent,
		Title:       "Voice message not transcribed",
		Description: channel + ": every speech backend failed; the agent received the audio untranscribed",
		Details:     string(details),
		EventID:     "voice:failed:" + channel,
	})
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
	modes := agentModes(al.GetConfig(), tmpDir)
	if err := os.MkdirAll(tmpDir, modes.Dir); err != nil {
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
		if err := copyFileContents(src, filepath.Join(tmpDir, name), modes.File); err != nil {
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
			if rmErr := os.Remove(filepath.Join(dir, e.Name())); rmErr != nil {
				logger.WarnCF("agent", "Failed to remove expired received media file", map[string]any{
					"path":  filepath.Join(dir, e.Name()),
					"error": rmErr.Error(),
				})
			}
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

// copyFileContents streams src to dst, creating (with mode) or truncating dst.
func copyFileContents(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src) //nolint:gosec // src is a media store path (ResolveWithMeta)
	if err != nil {
		return err
	}
	defer utils.CloseQuietly(in)
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode) //nolint:gosec // dst is <workspace>/tmp plus a name sanitized by receivedFileName
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		utils.CloseQuietly(out)
		return err
	}
	return out.Close()
}

// transcriptionFeedback is the echo of a message's transcripts: the text
// heard, a sentence naming the agent for audio that could not be
// transcribed, or "No voice detected in the audio" when nothing was heard.
func transcriptionFeedback(target *AgentInstance, transcriptions []voiceTranscript) string {
	var heard []string
	failed := false
	for _, t := range transcriptions {
		switch {
		case t.failed:
			failed = true
		case t.text != "":
			heard = append(heard, t.text)
		}
	}
	var lines []string
	if len(heard) > 0 {
		lines = append(lines, "Transcript: "+strings.Join(heard, "\n"))
	}
	if failed {
		name := "The assistant"
		if target != nil {
			name = target.DisplayName()
		}
		lines = append(lines, name+" couldn't understand your voice message.")
	}
	if len(lines) == 0 {
		return "No voice detected in the audio"
	}
	return strings.Join(lines, "\n")
}

// sendTranscriptionFeedback echoes feedbackMsg (transcriptionFeedback) to the
// user if the option is enabled. It uses Manager.SendMessage which executes
// synchronously (rate limiting, splitting, retry) so that ordering with the
// subsequent placeholder is guaranteed.
func (al *AgentLoop) sendTranscriptionFeedback(
	ctx context.Context,
	channel, chatID, messageID string,
	feedbackMsg string,
) {
	if cfg := al.GetConfig(); cfg == nil || !cfg.Voice.EchoTranscription {
		return
	}
	if al.channelManager == nil {
		return
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
