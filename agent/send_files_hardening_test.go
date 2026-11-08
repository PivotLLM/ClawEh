package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/media"
	"github.com/PivotLLM/ClawEh/tools"
	"github.com/PivotLLM/ClawEh/utils"
)

// refusingStore is a media store that takes nothing.
type refusingStore struct{ media.MediaStore }

func (refusingStore) Store(string, media.MediaMeta, string) (string, error) {
	return "", errors.New("store full")
}

// stagedFiles lists the media staging folder.
func stagedFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(utils.MediaTempDir())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func oneFile(content string) []tools.OutgoingFile {
	return []tools.OutgoingFile{{File: strings.NewReader(content), Filename: "notes.md", MaxSize: 100}}
}

// A file the media store will not take is reported, and its staged copy
// removed.
func TestSendFiles_StoreRefuses(t *testing.T) {
	l := newSendFilesLoop(t)
	l.al.mediaStore = refusingStore{l.store}
	results, err := l.al.SendFiles(context.Background(), "telegram", "42", oneFile("# notes"))
	if err != nil || len(results) != 1 || results[0] == nil || results[0].Error() != "could not be prepared: store full" {
		t.Fatalf("SendFiles = %v, %v", results, err)
	}
	if staged := stagedFiles(t); len(staged) != 0 {
		t.Fatalf("staged copies left: %v", staged)
	}
}

// A file that cannot be queued (the bus has stopped) is reported, and its
// staged copy released.
func TestSendFiles_BusClosed(t *testing.T) {
	l := newSendFilesLoop(t)
	l.al.bus.Close()
	results, err := l.al.SendFiles(context.Background(), "telegram", "42", oneFile("# notes"))
	if err != nil || len(results) != 1 || results[0] == nil || !strings.Contains(results[0].Error(), "could not be queued") {
		t.Fatalf("SendFiles = %v, %v", results, err)
	}
	if staged := stagedFiles(t); len(staged) != 0 {
		t.Fatalf("staged copies left: %v", staged)
	}
}

// A staging folder that cannot be made: the file "could not be read", and
// the agent is not told why (that is the operator's, in the log).
func TestSendFiles_StagingUnavailable(t *testing.T) {
	l := newSendFilesLoop(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	utils.SetMediaStagingDir(filepath.Join(blocker, "staging"), 0o700, 0o600)
	results, err := l.al.SendFiles(context.Background(), "telegram", "42", oneFile("# notes"))
	if err != nil || len(results) != 1 || results[0] == nil || results[0].Error() != "could not be read" {
		t.Fatalf("SendFiles = %v, %v", results, err)
	}
}

// A file that fails to read part way is not sent, and nothing is staged.
func TestSendFiles_ReadFails(t *testing.T) {
	l := newSendFilesLoop(t)
	results, err := l.al.SendFiles(context.Background(), "telegram", "42",
		[]tools.OutgoingFile{{File: failingReader{}, Filename: "notes.md", MaxSize: 100}})
	if err != nil || len(results) != 1 || results[0] == nil || results[0].Error() != "could not be read" {
		t.Fatalf("SendFiles = %v, %v", results, err)
	}
	if staged := stagedFiles(t); len(staged) != 0 {
		t.Fatalf("staged copies left: %v", staged)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("disk error") }

// A batch with one file refused before publishing: the others are still
// sent, and each has its own outcome, in order.
func TestSendFiles_MixedBatch(t *testing.T) {
	l := newSendFilesLoop(t)
	files := []tools.OutgoingFile{
		{File: strings.NewReader("too big for four"), Filename: "big.md", MaxSize: 4},
		{File: strings.NewReader("ok"), Filename: "ok.md", MaxSize: 4},
	}
	results, err := l.al.SendFiles(context.Background(), "telegram", "42", files)
	if err != nil || len(results) != 2 {
		t.Fatalf("SendFiles = %v, %v", results, err)
	}
	if results[0] == nil || !strings.Contains(results[0].Error(), "more than the limit of 4 bytes") || results[1] != nil {
		t.Fatalf("results = %v, want the first refused and the second sent", results)
	}
	if d := <-l.delivered; d.content != "ok" || d.msg.Parts[0].Filename != "ok.md" {
		t.Fatalf("delivered %+v (%q)", d.msg, d.content)
	}
}

// A turn stopped while its files wait for their reports: none is claimed
// sent.
func TestSendFiles_StoppedWhileWaiting(t *testing.T) {
	l := newSendFilesLoop(t)
	l.report = func(bus.OutboundMediaMessage) (error, bool) { return nil, false }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []error, 1)
	go func() {
		results, err := l.al.SendFiles(ctx, "telegram", "42", oneFile("# notes"))
		if err != nil {
			results = []error{err}
		}
		done <- results
	}()
	<-l.delivered
	cancel()
	if results := <-done; len(results) != 1 || !errors.Is(results[0], tools.ErrDeliveryUnconfirmed) {
		t.Fatalf("results = %v, want unconfirmed", results)
	}
}

// A chat whose channel the loop has no manager for cannot receive files.
func TestSendFiles_NoChannelManager(t *testing.T) {
	l := newSendFilesLoop(t)
	l.al.fileChannels = nil
	if _, err := l.al.SendFiles(context.Background(), "telegram", "42", oneFile("x")); !errors.Is(err, tools.ErrNoFileSupport) {
		t.Fatalf("err = %v, want ErrNoFileSupport", err)
	}
}

// Leftover copies: a missing staging folder is nothing to do; one that
// cannot be read is logged and left; only regular files named as copies go.
func TestRemoveStagedFileCopies_Edges(t *testing.T) {
	newSendFilesLoop(t)
	utils.SetMediaStagingDir(filepath.Join(t.TempDir(), "missing"), 0o700, 0o600)
	RemoveStagedFileCopies()

	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	utils.SetMediaStagingDir(blocker, 0o700, 0o600)
	RemoveStagedFileCopies()
	if _, err := os.Stat(blocker); err != nil {
		t.Fatalf("the unreadable staging path was touched: %v", err)
	}

	dir := t.TempDir()
	utils.SetMediaStagingDir(dir, 0o700, 0o600)
	if err := os.Mkdir(filepath.Join(dir, stagedPrefix+"folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	RemoveStagedFileCopies()
	if _, err := os.Stat(filepath.Join(dir, stagedPrefix+"folder")); err != nil {
		t.Fatalf("a folder named like a copy was removed: %v", err)
	}
}
