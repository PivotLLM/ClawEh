package tools

import (
	"context"
	"errors"
)

// OutgoingFile is one file a tool sends to a chat.
type OutgoingFile struct {
	// Path is the absolute host path of the file; the caller has checked that
	// the agent may read it.
	Path string
	// Filename is the name the chat shows.
	Filename string
	// ContentType is the file's MIME type.
	ContentType string
}

// FileSender sends files to a chat and reports how each delivery ended. The
// agent loop implements it; tools reach it through ToolDeps.Files.
type FileSender interface {
	// SendFiles sends files, in order, to chatID on channel and waits, for a
	// bounded time, for the channel to report each delivery. The result has
	// one entry per file: nil once the channel delivered it,
	// ErrDeliveryUnconfirmed when the channel had not reported by the
	// deadline, or the channel's reason. The error is ErrNoFileSupport, before
	// anything is sent, when the chat cannot receive files, or a failure to
	// hand the files to the channel at all.
	SendFiles(ctx context.Context, channel, chatID string, files []OutgoingFile) ([]error, error)
}

var (
	// ErrNoFileSupport is the refusal of a chat whose channel cannot send
	// files.
	ErrNoFileSupport = errors.New("this chat can't receive files")
	// ErrDeliveryUnconfirmed is a file the channel had not reported delivered
	// or failed when the wait ended.
	ErrDeliveryUnconfirmed = errors.New("not confirmed yet: still sending")
)
