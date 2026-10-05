package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuickCheckHealthyLiveDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.db")
	openWAL(t, path, 10) // stays open: quick_check must read past the writer
	if err := QuickCheck(path); err != nil {
		t.Fatalf("quick_check on a live WAL db: %v", err)
	}
}

func TestQuickCheckCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.db")
	// Enough bytes to be opened, but not a database.
	junk := strings.Repeat("this is not sqlite ", 100)
	if err := os.WriteFile(path, []byte(junk), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := QuickCheck(path); err == nil {
		t.Fatal("quick_check accepted garbage")
	}
	// A database whose page content was clobbered after creation.
	clobbered := filepath.Join(t.TempDir(), "clobbered.db")
	db := openWAL(t, clobbered, 200)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(clobbered, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte(strings.Repeat("\xff", 512)), 4096+100); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := QuickCheck(clobbered); err == nil {
		t.Fatal("quick_check accepted a clobbered page")
	}
}

func TestQuickCheckMissingFile(t *testing.T) {
	if err := QuickCheck(filepath.Join(t.TempDir(), "absent.db")); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestVacuumIntoRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	openWAL(t, src, 1)
	dst := filepath.Join(dir, "dst.db")
	if err := vacuumInto(src, dst); err != nil {
		t.Fatal(err)
	}
	if err := vacuumInto(src, dst); err == nil {
		t.Fatal("second VACUUM INTO onto an existing file must fail")
	}
}
