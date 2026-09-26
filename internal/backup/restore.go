// ClawEh
// License: MIT

package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/utils"
)

// LockFileName is the gateway's PID/lock file in CLAW_HOME. The gateway holds
// an exclusive flock on it while it runs (internal/gateway/lockfile.go); the
// restore refuses to touch a home whose lock is held.
const LockFileName = "claw.lock"

// ErrGatewayRunning is returned when a restore is attempted while the gateway
// holds the lock.
var ErrGatewayRunning = errors.New("the gateway is running; stop it before restoring")

// dbSidecars are the SQLite companions moved aside with a replaced database.
// A stale -wal left beside a restored file would be replayed into it.
var dbSidecars = [...]string{"-wal", "-shm", "-journal"}

// GatewayRunning reports whether a gateway holds the lock in home. A missing
// or stale (unlocked) lock file means not running.
func GatewayRunning(home string) (bool, error) {
	f, err := os.Open(filepath.Join(home, LockFileName)) //nolint:gosec // fixed name under CLAW_HOME
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("restore: open lock file: %w", err)
	}
	defer utils.CloseQuietly(f)
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil
		}
		return false, fmt.Errorf("restore: probe lock file: %w", err)
	}
	// Nobody held it; let go at once.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		return false, fmt.Errorf("restore: release probe lock: %w", err)
	}
	return false, nil
}

// PlanEntry is one file a restore will write.
type PlanEntry struct {
	Name     string      // archive path
	Target   string      // where it lands on disk
	Replaces bool        // a file already exists at Target
	Mode     fs.FileMode // permission bits recorded in the archive
	Size     int64
}

// Plan is what a restore of one archive into one home would do. Build it with
// PlanRestore, show it to the operator, then Apply it.
type Plan struct {
	Archive  string
	Home     string
	Manifest Manifest
	Entries  []PlanEntry
}

// PlanRestore reads the archive and resolves every entry against home. It
// touches nothing.
func PlanRestore(archive, home string) (*Plan, error) {
	p := &Plan{Archive: archive, Home: filepath.Clean(home)}
	m, err := readArchive(archive, func(hdr *tar.Header, _ io.Reader, m *Manifest) error {
		target, terr := targetFor(hdr.Name, p.Home, m)
		if terr != nil {
			return terr
		}
		e := PlanEntry{Name: hdr.Name, Target: target, Mode: fs.FileMode(hdr.Mode).Perm(), Size: hdr.Size} //nolint:gosec // tar modes fit
		if _, serr := os.Lstat(target); serr == nil {
			e.Replaces = true
		}
		p.Entries = append(p.Entries, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	p.Manifest = *m
	return p, nil
}

// Apply performs the restore: refuses while the gateway runs, extracts into a
// staging directory, runs quick_check on every restored database, and only then
// moves the files into place, parking whatever they replace (and any SQLite
// sidecars) under <home>/restore-backup-<stamp>/. A failed check aborts before
// anything is touched. Returns the restore-backup directory.
func (p *Plan) Apply(now time.Time) (string, error) {
	running, err := GatewayRunning(p.Home)
	if err != nil {
		return "", err
	}
	if running {
		return "", ErrGatewayRunning
	}
	stamp := now.Format(stampLayout)
	staging := filepath.Join(p.Home, restoreStagingPrefix+stamp)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return "", fmt.Errorf("restore: create staging: %w", err)
	}
	defer func() {
		if rmErr := os.RemoveAll(staging); rmErr != nil {
			logger.WarnCF("backup", "could not remove restore staging dir", map[string]any{"dir": staging, "error": rmErr.Error()})
		}
	}()

	// Extract everything into staging first.
	staged := map[string]string{} // archive name → staged path
	if _, err := readArchive(p.Archive, func(hdr *tar.Header, r io.Reader, _ *Manifest) error {
		dst := filepath.Join(staging, filepath.FromSlash(hdr.Name)) // names were validated by targetFor in PlanRestore
		if !within(staging, dst) {
			return fmt.Errorf("restore: unsafe archive path %q", hdr.Name)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return fmt.Errorf("restore: %w", err)
		}
		if err := writeFile(dst, r, hdr.Size, fs.FileMode(hdr.Mode).Perm()); err != nil { //nolint:gosec // tar modes fit
			return err
		}
		staged[hdr.Name] = dst
		return nil
	}); err != nil {
		return "", err
	}

	// Check every database before touching the live home.
	for _, e := range p.Entries {
		if s, ok := staged[e.Name]; ok && isDB(s) {
			if err := QuickCheck(s); err != nil {
				return "", fmt.Errorf("restore aborted, nothing was changed: restored database failed integrity check: %w", err)
			}
		}
	}

	// Swap.
	parked := filepath.Join(p.Home, restoreBackupPrefix+stamp)
	if err := os.MkdirAll(parked, 0o700); err != nil {
		return "", fmt.Errorf("restore: create %s: %w", parked, err)
	}
	for _, e := range p.Entries {
		s, ok := staged[e.Name]
		if !ok {
			return parked, fmt.Errorf("restore: %s is in the plan but not in the archive", e.Name)
		}
		if err := park(e.Target, filepath.Join(parked, filepath.FromSlash(e.Name))); err != nil {
			return parked, err
		}
		if err := os.MkdirAll(filepath.Dir(e.Target), 0o700); err != nil {
			return parked, fmt.Errorf("restore: %w", err)
		}
		if err := moveFile(s, e.Target); err != nil {
			return parked, err
		}
	}
	return parked, nil
}

// park moves target (if it exists) and its SQLite sidecars to dst.
func park(target, dst string) error {
	for _, suffix := range append([]string{""}, dbSidecars[:]...) {
		src := target + suffix
		if _, err := os.Lstat(src); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("restore: stat %s: %w", src, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return fmt.Errorf("restore: %w", err)
		}
		if err := moveFile(src, dst+suffix); err != nil {
			return err
		}
	}
	return nil
}

// moveFile renames src to dst, falling back to copy-and-remove across
// filesystems (an external agents directory may be one).
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("restore: stat %s: %w", src, err)
	}
	in, err := os.Open(src) //nolint:gosec // both ends are paths this package resolved
	if err != nil {
		return fmt.Errorf("restore: open %s: %w", src, err)
	}
	defer utils.CloseQuietly(in)
	if err := writeFile(dst, in, info.Size(), info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("restore: remove %s: %w", src, err)
	}
	return nil
}

// writeFile creates dst with mode and copies exactly size bytes from r.
func writeFile(dst string, r io.Reader, size int64, mode fs.FileMode) error {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode) //nolint:gosec // dst was checked to lie under the staging dir or a plan target
	if err != nil {
		return fmt.Errorf("restore: create %s: %w", dst, err)
	}
	if _, err := io.CopyN(f, r, size); err != nil {
		utils.CloseQuietly(f)
		return fmt.Errorf("restore: write %s: %w", dst, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("restore: close %s: %w", dst, err)
	}
	return os.Chmod(dst, mode) // O_CREATE applies the umask; the archive's mode wins
}

// targetFor maps an archive path to its place on disk: under home, or under the
// manifest's agents_dir for external/agents/ entries. Absolute paths and any
// ".." component are rejected.
func targetFor(name, home string, m *Manifest) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || clean == "." || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) ||
		strings.Contains(clean, string(filepath.Separator)+".."+string(filepath.Separator)) {
		return "", fmt.Errorf("restore: unsafe archive path %q", name)
	}
	if rest, ok := strings.CutPrefix(name, externalAgentsPrefix); ok {
		if m.AgentsDir == "" {
			return "", fmt.Errorf("restore: %q is an external agents entry but the manifest names no agents_dir", name)
		}
		return filepath.Join(m.AgentsDir, filepath.FromSlash(rest)), nil
	}
	return filepath.Join(home, clean), nil
}

// readArchive opens a backup tarball, decodes the manifest (which must be the
// first entry) and calls fn for every regular file after it. Returns the
// manifest.
func readArchive(archive string, fn func(hdr *tar.Header, r io.Reader, m *Manifest) error) (*Manifest, error) {
	f, err := os.Open(archive) //nolint:gosec // operator-supplied archive path
	if err != nil {
		return nil, fmt.Errorf("restore: open %s: %w", archive, err)
	}
	defer utils.CloseQuietly(f)
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("restore: %s is not a gzip archive: %w", archive, err)
	}
	defer utils.CloseQuietly(gz)
	tr := tar.NewReader(gz)

	hdr, err := tr.Next()
	if err != nil {
		return nil, fmt.Errorf("restore: %s is not a tar archive: %w", archive, err)
	}
	if hdr.Name != ManifestName {
		return nil, fmt.Errorf("restore: %s is not a ClawEh backup (first entry %q, want %s)", archive, hdr.Name, ManifestName)
	}
	var m Manifest
	if decErr := json.NewDecoder(io.LimitReader(tr, hdr.Size)).Decode(&m); decErr != nil {
		return nil, fmt.Errorf("restore: manifest: %w", decErr)
	}
	if m.Version != ManifestVersion {
		return nil, fmt.Errorf("restore: manifest version %d, this build reads %d", m.Version, ManifestVersion)
	}
	for {
		hdr, err = tr.Next()
		if errors.Is(err, io.EOF) {
			return &m, nil
		}
		if err != nil {
			return nil, fmt.Errorf("restore: read %s: %w", archive, err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if err := fn(hdr, tr, &m); err != nil {
			return nil, err
		}
	}
}
