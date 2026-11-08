package tools

import (
	"context"
	"errors"
	"io"
)

// OutgoingFile is one file a tool sends to a chat.
type OutgoingFile struct {
	// File is the open file to send, read from where it is; the caller has
	// checked that the agent may read it and closes it after SendFiles.
	File io.Reader
	// Filename is the name the chat shows.
	Filename string
	// MaxSize is the most bytes the file may have; a file that has grown
	// past it since it was checked is not sent.
	MaxSize int64
}

// FileSender sends files to a chat and reports how each delivery ended. The
// agent loop implements it; tools reach it through ToolDeps.Files.
type FileSender interface {
	// SendFiles copies files, in order, before it returns, sends them to
	// chatID on channel and waits, for a bounded time, for the channel to
	// report each delivery. The result has one entry per file: nil once the
	// channel delivered it, ErrDeliveryUnconfirmed when the channel had not
	// reported by the deadline, or the reason it was not sent. The error is
	// ErrNoFileSupport, before anything is sent, when the chat cannot
	// receive files, or a failure to hand the files to the channel at all.
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

// ReplyChat is where the final reply of the turn a tool call belongs to
// goes: the chat files are sent to.
type ReplyChat struct {
	Channel string
	ChatID  string
	// Ambiguous marks a call that cannot be tied to one turn: over the MCP
	// host, a conversation token while more than one turn of its session
	// runs.
	Ambiguous bool
}

type replyChatKey struct{}

// WithReplyChat returns ctx carrying the reply chat of the call's turn.
func WithReplyChat(ctx context.Context, rc ReplyChat) context.Context {
	return context.WithValue(ctx, replyChatKey{}, rc)
}

// ReplyChatOf returns the reply chat of the call's turn; false when the call
// belongs to no turn (a service token, a call outside a turn).
func ReplyChatOf(ctx context.Context) (ReplyChat, bool) {
	rc, ok := ctx.Value(replyChatKey{}).(ReplyChat)
	return rc, ok
}
