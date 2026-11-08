package msg

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/h2non/filetype"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/tools"
)

// MaxSendFiles is the most files one msg_send_file call sends.
const MaxSendFiles = 10

// FileReader is the agent's file-reading permission, as the file tools apply
// it (tools/files.Reader).
type FileReader interface {
	// Resolve returns the absolute host path of a path the agent may read.
	Resolve(ref string) (string, error)
	// Allowed reports whether the agent may read an absolute host path.
	Allowed(abs string) error
}

// SendFileTool sends files the agent may read to the chat its turn answers:
// where the turn's final reply goes.
type SendFileTool struct {
	reader      FileReader
	maxFileSize int
	sender      tools.FileSender
}

// NewSendFileTool returns the msg_send_file tool reading through reader and
// sending through sender. A maxFileSize of 0 or less is the default media
// size limit.
func NewSendFileTool(reader FileReader, maxFileSize int, sender tools.FileSender) *SendFileTool {
	if maxFileSize <= 0 {
		maxFileSize = config.DefaultMaxMediaSize
	}
	return &SendFileTool{reader: reader, maxFileSize: maxFileSize, sender: sender}
}

func (t *SendFileTool) Name() string { return "msg_send_file" }

func (t *SendFileTool) Description() string {
	return "Send files to the chat you are answering. Only files you can read with the file tools can be sent."
}

func (t *SendFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"files": map[string]any{
				"type":        "array",
				"description": fmt.Sprintf("The files to send, in order (1 to %d).", MaxSendFiles),
				"minItems":    1,
				"maxItems":    MaxSendFiles,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Path of the file, as the file tools take it.",
						},
						"filename": map[string]any{
							"type":        "string",
							"description": "Name the chat shows. Defaults to the file's own name.",
						},
					},
					"required":             []string{"path"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"files"},
		"additionalProperties": false,
	}
}

// fileRequest is one entry of the files argument.
type fileRequest struct {
	path     string
	filename string
}

func (t *SendFileTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	requests, problem := parseFileRequests(args)
	if problem != "" {
		return refused(problem)
	}

	channel, chatID := tools.ToolChannel(ctx), tools.ToolChatID(ctx)
	switch {
	case constants.RepliesToAgent(channel):
		return refused("Files can't be sent to another agent.")
	case channel == "" || chatID == "":
		return refused("There is no chat to send files to.")
	case t.sender == nil || t.reader == nil:
		return tools.ErrorResult("Sending files is not available.")
	}

	files := make([]tools.OutgoingFile, 0, len(requests))
	for _, req := range requests {
		f, problem := t.checkFile(req)
		if problem != "" {
			return refused(problem + " Nothing was sent.")
		}
		files = append(files, f)
	}

	results, err := t.sender.SendFiles(ctx, channel, chatID, files)
	switch {
	case errors.Is(err, tools.ErrNoFileSupport):
		return refused("This chat can't receive files.")
	case err != nil:
		return tools.ErrorResult(fmt.Sprintf("The files could not be sent: %v.", err)).WithError(err)
	}
	return sendReport(files, results)
}

// parseFileRequests reads the files argument; problem is the refusal when it
// is missing or malformed.
func parseFileRequests(args map[string]any) (requests []fileRequest, problem string) {
	if _, ok := args["path"]; ok {
		return nil, `"path" is no longer accepted: pass "files", a list of {"path", "filename"}.`
	}
	raw, ok := args["files"].([]any)
	if !ok || len(raw) == 0 {
		return nil, `"files" is required: a list of {"path", "filename"}.`
	}
	if len(raw) > MaxSendFiles {
		return nil, fmt.Sprintf("At most %d files can be sent at once.", MaxSendFiles)
	}
	for i, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Sprintf(`files[%d] must be an object with "path".`, i)
		}
		for key := range entry {
			if key != "path" && key != "filename" {
				return nil, fmt.Sprintf(`files[%d] has an unknown field %q.`, i, key)
			}
		}
		path, ok := entry["path"].(string)
		if !ok || strings.TrimSpace(path) == "" {
			return nil, fmt.Sprintf(`files[%d] needs a "path".`, i)
		}
		filename, ok := entry["filename"].(string)
		if _, present := entry["filename"]; present && !ok {
			return nil, fmt.Sprintf(`files[%d].filename must be a string.`, i)
		}
		filename = strings.TrimSpace(filename)
		if filename != "" && (strings.ContainsAny(filename, `/\`) || filename == "." || filename == "..") {
			return nil, fmt.Sprintf("files[%d].filename must be a plain file name, not a path.", i)
		}
		requests = append(requests, fileRequest{path: path, filename: filename})
	}
	return requests, ""
}

// checkFile applies the agent's file-reading permission to req, exactly as
// the file tools do, to the path and to the file it finally names (a
// symbolic link's target), and the size limit. problem names the path when
// the file may not be sent.
func (t *SendFileTool) checkFile(req fileRequest) (tools.OutgoingFile, string) {
	cannotRead := fmt.Sprintf("Can't send %q: you can't read it, or it does not exist.", req.path)
	abs, err := t.reader.Resolve(req.path)
	if err != nil {
		return tools.OutgoingFile{}, cannotRead
	}
	target, err := filepath.EvalSymlinks(abs)
	if err != nil || t.reader.Allowed(target) != nil {
		return tools.OutgoingFile{}, cannotRead
	}
	info, err := os.Stat(target)
	switch {
	case err != nil:
		return tools.OutgoingFile{}, cannotRead
	case info.IsDir():
		return tools.OutgoingFile{}, fmt.Sprintf("Can't send %q: it is a folder.", req.path)
	case !info.Mode().IsRegular():
		return tools.OutgoingFile{}, fmt.Sprintf("Can't send %q: it is not a regular file.", req.path)
	case info.Size() > int64(t.maxFileSize):
		return tools.OutgoingFile{}, fmt.Sprintf("Can't send %q: it is %d bytes, more than the limit of %d bytes.",
			req.path, info.Size(), t.maxFileSize)
	}
	filename := req.filename
	if filename == "" {
		filename = filepath.Base(abs)
	}
	return tools.OutgoingFile{Path: target, Filename: filename, ContentType: detectMediaType(target)}, ""
}

// sendReport is the result of a send: every file sent, or each file's
// outcome. It is an error when any file was not sent.
func sendReport(files []tools.OutgoingFile, results []error) *tools.ToolResult {
	var b strings.Builder
	failed, pending := false, false
	for i, f := range files {
		var err error
		if i < len(results) {
			err = results[i]
		}
		switch {
		case err == nil:
			fmt.Fprintf(&b, "%s: sent\n", f.Filename)
		case errors.Is(err, tools.ErrDeliveryUnconfirmed):
			pending = true
			fmt.Fprintf(&b, "%s: %v\n", f.Filename, err)
		default:
			failed = true
			fmt.Fprintf(&b, "%s: not sent: %v\n", f.Filename, err)
		}
	}
	text := strings.TrimSuffix(b.String(), "\n")
	if !failed && !pending {
		return tools.NewToolResult("Sent to the chat:\n" + text)
	}
	if failed {
		return tools.ErrorResult(text)
	}
	return tools.NewToolResult(text)
}

// detectMediaType determines the MIME type of a file.
// Uses magic-bytes detection (h2non/filetype) first, then falls back to
// extension-based lookup via mime.TypeByExtension.
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
