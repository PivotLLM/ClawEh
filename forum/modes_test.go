//go:build !windows

package forum

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/google/uuid"

	"github.com/PivotLLM/ClawEh/forum/forumfs"
)

// A store created with the host's modes (Host.Modes) gives every folder and
// file it creates exactly those modes, even under a umask that would narrow
// them: the base, the forum folder, a run's folders, forum.json, the lock
// file and the transcript.
func TestStore_HostModes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	base := filepath.Join(t.TempDir(), "forums")
	p := filePerms{dir: 0o750, file: 0o640}
	s, err := createStoreWith(base, uuid.NewString(), p)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Lock(); err != nil {
		t.Fatal(err)
	}
	defer s.Unlock()
	if err = s.WriteForumConfig([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(1)
	if err != nil {
		t.Fatal(err)
	}
	if err = run.AppendTranscript("hello\n"); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		base:                                  0o750,
		filepath.Join(base, forumfs.LocksDir): 0o750,
		s.Dir():                               0o750,
		filepath.Join(s.Dir(), dirRuns, "1"):  0o750,
		filepath.Join(s.Dir(), dirRuns, "1", dirLayers): 0o750,
		filepath.Join(s.Dir(), fileConfig):              0o640,
		s.lockPath():                                    0o640,
		run.Path(fileTranscript):                        0o640,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s = %04o, want %04o", path, got, want)
		}
	}
}

// The service takes a forum's modes from Host.Modes for its base, and falls
// back to 0700/0600 when the host sets none or a zero mode.
func TestService_PermsFor(t *testing.T) {
	s := &Service{host: Host{Modes: func(base string) (os.FileMode, os.FileMode) {
		if base == "/w/alice/forums" {
			return 0o750, 0o640
		}
		return 0, 0
	}}}
	if got := s.permsFor("/w/alice/forums"); got != (filePerms{dir: 0o750, file: 0o640}) {
		t.Errorf("permsFor = %+v, want 0750/0640", got)
	}
	if got := s.permsFor("/elsewhere"); got != defaultPerms {
		t.Errorf("zero modes = %+v, want the defaults", got)
	}
	if got := (&Service{}).permsFor("/x"); got != defaultPerms {
		t.Errorf("no Modes = %+v, want the defaults", got)
	}
}
