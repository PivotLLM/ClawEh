// ClawEh
// License: MIT

// Package backup writes a dated tarball of everything ClawEh needs to come back
// on a new host — config.json, the cron jobs file, state/ (tokens and the device
// pairing database), credentials.json and tls/ when present, and every SQLite
// database under CLAW_HOME (session archives, cognitive memory, the fusion
// OAuth store) — and restores one. It is driven on a nightly schedule by the
// gateway, on demand from the WebUI, and by `claw backup` / `claw restore`.
//
// SQLite files are never copied with plain file I/O: a live WAL database is
// two files plus a lock, and a byte copy of the main file can miss committed
// rows or be torn. Each database is checked with PRAGMA quick_check and then
// copied with VACUUM INTO on a read-only connection, which takes a consistent
// snapshot without disturbing the running store.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/alerts"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/utils"
)

const (
	// ArchivePrefix and ArchiveSuffix frame the archive name:
	// claw-backup-YYYYMMDD-HHMMSS.tar.gz.
	ArchivePrefix = "claw-backup-"
	ArchiveSuffix = ".tar.gz"

	// ManifestName is the first entry in every archive: what was backed up,
	// from where, and when.
	ManifestName = "manifest.json"

	// ManifestVersion is bumped when the archive layout changes.
	ManifestVersion = 1

	// externalAgentsPrefix is where agent workspaces live inside the archive
	// when agents.base_dir points outside CLAW_HOME; the manifest records the
	// real directory so a restore can put them back.
	externalAgentsPrefix = "external/agents/"

	// dateLayout is the legacy per-day backup folder name (kept for pruning
	// folders written by versions before the tarball).
	dateLayout = "20060102"

	// stampLayout is the archive's timestamp.
	stampLayout = "20060102-150405"

	// restoreBackupPrefix names the directory a restore moves the replaced
	// files into: <CLAW_HOME>/restore-backup-<stamp>/.
	restoreBackupPrefix = "restore-backup-"

	// restoreStagingPrefix names the directory a restore extracts into before
	// checking and swapping: <CLAW_HOME>/.restore-staging-<stamp>/.
	restoreStagingPrefix = ".restore-staging-"

	credentialsFile = "credentials.json"
	tlsDir          = "tls"
	stateDir        = "state"
	configFile      = "config.json"
	cronJobsFile    = "jobs.json"
)

// ExcludedDirs are directory names skipped anywhere in the database walk: the
// media staging cache, logs, the backup destination itself, per-agent scratch
// space, and cogmem's throwaway per-sub-agent snapshots. Nothing in them is
// needed to bring an install back.
var ExcludedDirs = [...]string{"media", "logs", "backup", "tmp", "subagents"}

// excludedPrefixes are top-level directory name prefixes skipped in the walk:
// what a restore leaves behind, and its own staging area.
var excludedPrefixes = [...]string{restoreBackupPrefix, restoreStagingPrefix}

// dbExtensions marks a file as a SQLite database. Sidecar files (-wal, -shm,
// -journal) do not match and are never archived: VACUUM INTO folds the WAL in.
var dbExtensions = [...]string{".db", ".sqlite", ".sqlite3"}

// Source is what a backup reads.
type Source struct {
	Home       string // CLAW_HOME
	ConfigPath string // config.json (normally <Home>/config.json)
	CronDir    string // holds jobs.json
	AgentsDir  string // agent workspaces; walked separately only when outside Home
}

// Options tune a run. The zero value is what the nightly scheduler uses.
type Options struct {
	// Dest is the directory the archive is written to. Empty means
	// backup.dest from the config, or <CLAW_HOME>/backup when that is unset.
	Dest string
	// Alerter receives the alert for a database that failed quick_check.
	// nil means the process default.
	Alerter alerter.Alerter
}

// Manifest is the archive's manifest.json.
type Manifest struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	Home      string    `json:"claw_home"`
	AgentsDir string    `json:"agents_dir,omitempty"` // set only when outside claw_home
	Files     []string  `json:"files"`                // archive paths, in archive order
}

// Skipped is a database left out of the archive because it failed quick_check.
type Skipped struct {
	Path string
	Err  error
}

// Result describes a finished run.
type Result struct {
	Archive string    // the tarball's path
	Bytes   int64     // its size
	Files   int       // entries written, not counting the manifest
	Skipped []Skipped // databases left out
}

// entry is one file to archive: where it is read from, and its archive path.
type entry struct {
	src  string // file on disk
	name string // path inside the archive (slash-separated)
	db   bool   // copy with VACUUM INTO rather than plain read
}

// Run writes <dest>/claw-backup-<stamp>.tar.gz from src. dest is created 0700
// when missing; the archive is 0600. Databases that fail quick_check are
// skipped (recorded in Result.Skipped and alerted), never fatal: one bad file
// must not cost the operator the rest of the backup.
func Run(src Source, dest string, now time.Time, a alerter.Alerter) (*Result, error) {
	if a == nil {
		a = alerts.Default()
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return nil, fmt.Errorf("backup: create %s: %w", dest, err)
	}
	if err := os.Chmod(dest, 0o700); err != nil {
		// A network mount may not honour it; the archive itself is still 0600.
		logger.WarnCF("backup", "could not restrict backup directory", map[string]any{"dir": dest, "error": err.Error()})
	}

	entries, external, err := collect(src, dest)
	if err != nil {
		return nil, err
	}

	stamp := now.Format(stampLayout)
	final := filepath.Join(dest, ArchivePrefix+stamp+ArchiveSuffix)
	tmpDir, err := os.MkdirTemp(dest, ".claw-backup-*")
	if err != nil {
		return nil, fmt.Errorf("backup: temp dir: %w", err)
	}
	defer func() {
		if rmErr := os.RemoveAll(tmpDir); rmErr != nil {
			logger.WarnCF("backup", "could not remove temp dir", map[string]any{"dir": tmpDir, "error": rmErr.Error()})
		}
	}()

	res := &Result{}
	m := Manifest{Version: ManifestVersion, CreatedAt: now, Home: src.Home}
	if external {
		m.AgentsDir = src.AgentsDir
	}

	// Pass 1: snapshot the databases and settle the file list, so the manifest
	// written at the front of the archive is exact.
	type ready struct {
		entry
		path string // what is actually read: the VACUUM copy for databases
	}
	var files []ready
	for i, e := range entries {
		r := ready{entry: e, path: e.src}
		if e.db {
			if qerr := QuickCheck(e.src); qerr != nil {
				res.Skipped = append(res.Skipped, Skipped{Path: e.src, Err: qerr})
				logger.ErrorCF("backup", "database failed quick_check; left out of the backup",
					map[string]any{"path": e.src, "error": qerr.Error()})
				a.Send(alerter.Alert{
					Title:       "Database failed integrity check",
					Description: e.src + " failed PRAGMA quick_check and was left out of the backup; the store may be corrupt",
					Details:     qerr.Error(),
					EventID:     "backup:" + e.name,
				})
				continue
			}
			r.path = filepath.Join(tmpDir, fmt.Sprintf("%d.db", i))
			if verr := vacuumInto(e.src, r.path); verr != nil {
				return nil, fmt.Errorf("backup: %w", verr)
			}
		}
		files = append(files, r)
		m.Files = append(m.Files, e.name)
	}

	tmpArchive := filepath.Join(tmpDir, "archive.tar.gz")
	if wErr := writeArchive(tmpArchive, m, func(tw *tar.Writer) error {
		for _, f := range files {
			if addErr := addFile(tw, f.path, f.src, f.name); addErr != nil {
				return addErr
			}
			res.Files++
		}
		return nil
	}); wErr != nil {
		return nil, wErr
	}
	if mvErr := os.Rename(tmpArchive, final); mvErr != nil {
		return nil, fmt.Errorf("backup: place %s: %w", final, mvErr)
	}
	info, err := os.Stat(final)
	if err != nil {
		return nil, fmt.Errorf("backup: stat %s: %w", final, err)
	}
	res.Archive = final
	res.Bytes = info.Size()
	return res, nil
}

// writeArchive creates path (0600) as a gzip tarball whose first entry is the
// manifest, then hands the writer to body for the files.
func writeArchive(path string, m Manifest, body func(*tar.Writer) error) (err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // path is inside the temp dir this package created
	if err != nil {
		return fmt.Errorf("backup: create %s: %w", path, err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	defer func() {
		for _, c := range []io.Closer{tw, gz, f} {
			if cerr := c.Close(); cerr != nil && err == nil {
				err = fmt.Errorf("backup: close %s: %w", path, cerr)
			}
		}
	}()

	mj, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("backup: manifest: %w", err)
	}
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg, Name: ManifestName, Mode: 0o600,
		Size: int64(len(mj)), ModTime: m.CreatedAt, Format: tar.FormatPAX,
	}); err != nil {
		return fmt.Errorf("backup: manifest header: %w", err)
	}
	if _, err := tw.Write(mj); err != nil {
		return fmt.Errorf("backup: manifest body: %w", err)
	}
	return body(tw)
}

// addFile appends the file at readPath to the archive under name, carrying the
// mode and mtime of orig (the live file, which differs from readPath for a
// database snapshot).
func addFile(tw *tar.Writer, readPath, orig, name string) error {
	oi, err := os.Stat(orig)
	if err != nil {
		return fmt.Errorf("backup: stat %s: %w", orig, err)
	}
	f, err := os.Open(readPath) //nolint:gosec // paths come from the walk of CLAW_HOME or this package's temp dir
	if err != nil {
		return fmt.Errorf("backup: open %s: %w", readPath, err)
	}
	defer utils.CloseQuietly(f)
	ri, err := f.Stat()
	if err != nil {
		return fmt.Errorf("backup: stat %s: %w", readPath, err)
	}
	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     name,
		Mode:     int64(oi.Mode().Perm()),
		Size:     ri.Size(),
		ModTime:  oi.ModTime(),
		Format:   tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("backup: header %s: %w", name, err)
	}
	if _, err := io.CopyN(tw, f, ri.Size()); err != nil {
		return fmt.Errorf("backup: write %s: %w", name, err)
	}
	return nil
}

// collect lists what to archive, in a stable order: config.json, cron/jobs.json,
// credentials.json, tls/, state/, then every database found walking Home (and
// AgentsDir when it lies outside Home, reported by the second result). Missing
// optional files are silently absent. dest is excluded from the walk when it is
// under Home, so a backup never archives older backups.
func collect(src Source, dest string) ([]entry, bool, error) {
	var out []entry
	seen := map[string]bool{}
	add := func(e entry) {
		if !seen[e.name] {
			seen[e.name] = true
			out = append(out, e)
		}
	}
	addIfFile := func(path, name string) error {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("backup: stat %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		add(entry{src: path, name: name, db: isDB(path)})
		return nil
	}

	if err := addIfFile(src.ConfigPath, configFile); err != nil {
		return nil, false, err
	}
	if err := addIfFile(filepath.Join(src.CronDir, cronJobsFile), "cron/"+cronJobsFile); err != nil {
		return nil, false, err
	}
	if err := addIfFile(filepath.Join(src.Home, credentialsFile), credentialsFile); err != nil {
		return nil, false, err
	}
	// Whole directories: every regular file, databases via VACUUM. SQLite
	// sidecars are skipped: the VACUUM copy already holds what the WAL holds,
	// and a byte copy of a live -wal is the torn copy this package exists to
	// avoid.
	for _, d := range []string{tlsDir, stateDir} {
		if err := walk(filepath.Join(src.Home, d), d+"/", dest, func(path, name string) {
			if isDBSidecar(path) {
				return
			}
			add(entry{src: path, name: name, db: isDB(path)})
		}); err != nil {
			return nil, false, err
		}
	}
	// Databases anywhere else under Home.
	dbOnly := func(path, name string) {
		if isDB(path) {
			add(entry{src: path, name: name, db: true})
		}
	}
	if err := walk(src.Home, "", dest, dbOnly); err != nil {
		return nil, false, err
	}
	external := false
	if src.AgentsDir != "" && !within(src.Home, src.AgentsDir) {
		external = true
		if err := walk(src.AgentsDir, externalAgentsPrefix, dest, dbOnly); err != nil {
			return nil, false, err
		}
	}
	return out, external, nil
}

// walk calls fn(path, archiveName) for every regular file under root, skipping
// ExcludedDirs, excludedPrefixes (at the top level), symlinks, and skip itself.
// A missing root is not an error.
func walk(root, prefix, skip string, fn func(path, name string)) error {
	root = filepath.Clean(root)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root && os.IsNotExist(err) {
				return filepath.SkipAll
			}
			return err
		}
		if path == root {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			if path == filepath.Clean(skip) || excludedDir(d.Name(), rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fn(path, prefix+filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return fmt.Errorf("backup: walk %s: %w", root, err)
	}
	return nil
}

// excludedDir reports whether a directory named name (at rel under the walk
// root) is skipped.
func excludedDir(name, rel string) bool {
	for _, x := range ExcludedDirs {
		if name == x {
			return true
		}
	}
	if !strings.ContainsRune(rel, filepath.Separator) { // top level only
		for _, p := range excludedPrefixes {
			if strings.HasPrefix(name, p) {
				return true
			}
		}
	}
	return false
}

// isDB reports whether path has a SQLite database extension.
func isDB(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, x := range dbExtensions {
		if ext == x {
			return true
		}
	}
	return false
}

// isDBSidecar reports whether path is a SQLite -wal, -shm or -journal file.
func isDBSidecar(path string) bool {
	for _, s := range dbSidecars {
		if strings.HasSuffix(path, s) {
			return true
		}
	}
	return false
}

// within reports whether path is dir or lies under it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// DestFor resolves the backup directory: an explicit override, else
// backup.dest from the config, else <CLAW_HOME>/backup.
func DestFor(cfg *config.Config, override string) string {
	if override != "" {
		return override
	}
	if d := strings.TrimSpace(cfg.Backup.Dest); d != "" {
		return d
	}
	return filepath.Join(cfg.DataDir(), "backup")
}

// RunForConfig backs up the install described by cfg (config.json at
// configPath) into the resolved destination and prunes archives past the
// retention window. Used by the nightly scheduler, the WebUI trigger and
// `claw backup`.
func RunForConfig(cfg *config.Config, configPath string, now time.Time, opts Options) (*Result, error) {
	dest := DestFor(cfg, opts.Dest)
	src := Source{
		Home:       cfg.DataDir(),
		ConfigPath: configPath,
		CronDir:    cfg.CronPath(),
		AgentsDir:  cfg.BaseDir(),
	}
	res, err := Run(src, dest, now, opts.Alerter)
	if err != nil {
		return nil, err
	}
	if _, perr := Prune(dest, cfg.Backup.BackupRetainDays(), now); perr != nil {
		logger.WarnCF("backup", "prune failed", map[string]any{"error": perr.Error()})
	}
	return res, nil
}

// Prune removes archives (claw-backup-<stamp>.tar.gz) and legacy day-folders
// (YYYYMMDD) under dest whose date is older than retainDays. retainDays <= 0
// disables pruning. Anything else in the directory is left alone. Returns the
// number of items removed.
func Prune(dest string, retainDays int, now time.Time) (int, error) {
	if retainDays <= 0 {
		return 0, nil
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("backup prune: read %s: %w", dest, err)
	}
	cutoff := now.AddDate(0, 0, -retainDays)
	cutoffDay := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, cutoff.Location())
	removed := 0
	for _, e := range entries {
		var day time.Time
		var perr error
		switch {
		case e.IsDir():
			day, perr = time.ParseInLocation(dateLayout, e.Name(), now.Location())
		case strings.HasPrefix(e.Name(), ArchivePrefix) && strings.HasSuffix(e.Name(), ArchiveSuffix):
			stamp := strings.TrimSuffix(strings.TrimPrefix(e.Name(), ArchivePrefix), ArchiveSuffix)
			day, perr = time.ParseInLocation(stampLayout, stamp, now.Location())
		default:
			continue
		}
		if perr != nil || !day.Before(cutoffDay) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dest, e.Name())); err != nil {
			logger.WarnCF("backup", "failed to prune old backup", map[string]any{
				"name": e.Name(), "error": err.Error(),
			})
			continue
		}
		removed++
	}
	return removed, nil
}
