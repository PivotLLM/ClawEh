// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/PivotLLM/ClawEh/forum/forumfs"
)

// File-system primitives of the store: confined, symlink-refusing access
// through os.Root, atomic and durable writes, and the record encoding.

// notFound maps a missing file to ErrNotFound and keeps other errors.
func notFound(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

// marshalRecord is the JSON form every record is written in: indented,
// newline-terminated and without HTML escaping (a "<" stays "<"), so the
// files read well by hand.
func marshalRecord(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// marshalCompact is json.Marshal without HTML escaping.
func marshalCompact(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// digest is the hex SHA-256 of data.
func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// inRoot runs fn with the run's directory opened as an os.Root, so nothing
// fn does can reach outside it. On the forum handle (no run) it fails.
func (s *forumStore) inRoot(fn func(r *os.Root) error) error {
	if s.root == "" {
		return fmt.Errorf("forum %s: %w: not a run", s.id, ErrInvalidState)
	}
	r, err := os.OpenRoot(s.root)
	if err != nil {
		return err
	}
	err = fn(r)
	if closeErr := r.Close(); err == nil {
		err = closeErr
	}
	return err
}

// inDir runs fn with the forum directory opened as an os.Root.
func (s *forumStore) inDir(fn func(r *os.Root) error) error {
	r, err := os.OpenRoot(s.dir)
	if err != nil {
		return err
	}
	err = fn(r)
	if closeErr := r.Close(); err == nil {
		err = closeErr
	}
	return err
}

// writeRel writes data to the root-relative rel (writeFileAt).
func (s *forumStore) writeRel(rel string, data []byte, exclusive bool) error {
	clean, err := rootRel(rel)
	if err != nil {
		return fmt.Errorf("write forum file: %w", err)
	}
	return s.inRoot(func(r *os.Root) error { return writeFileAt(r, clean, data, exclusive) })
}

// rootRel cleans a root-relative path, refusing one that is absolute or
// escapes the root.
func rootRel(rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if rel == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the forum root", rel)
	}
	return clean, nil
}

// noSymlinks checks that every existing directory on the way to rel (rel
// itself excluded) is a real directory, not a symbolic link. Missing
// components are not an error; the operation that needs them fails.
func noSymlinks(r *os.Root, rel string) error {
	dir := filepath.Dir(rel)
	if dir == "." {
		return nil
	}
	cur := ""
	for elem := range strings.SplitSeq(dir, string(filepath.Separator)) {
		cur = filepath.Join(cur, elem)
		fi, err := r.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link", cur)
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s is not a directory", cur)
		}
	}
	return nil
}

// lstatRegular checks that rel, reached without a symbolic link, is a
// regular file. A missing file wraps fs.ErrNotExist.
func lstatRegular(r *os.Root, rel string) error {
	if err := noSymlinks(r, rel); err != nil {
		return err
	}
	fi, err := r.Lstat(rel)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", rel)
	}
	return nil
}

// mkdirAt creates the root-relative dir (and missing parents, 0700),
// fsyncing the parent of each directory it creates. An existing component
// that is a symbolic link or not a directory fails.
func mkdirAt(r *os.Root, dir string) error {
	clean, err := rootRel(dir)
	if err != nil {
		return err
	}
	if clean == "." {
		return nil
	}
	parent := "."
	for elem := range strings.SplitSeq(clean, string(filepath.Separator)) {
		cur := filepath.Join(parent, elem)
		fi, err := r.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err = r.Mkdir(cur, dirPerm); err != nil {
				return err
			}
			if err = syncDirAt(r, parent); err != nil {
				return err
			}
		case err != nil:
			return err
		case fi.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s is a symbolic link", cur)
		case !fi.IsDir():
			return fmt.Errorf("%s is not a directory", cur)
		}
		parent = cur
	}
	return nil
}

// writeFileAt writes data to the r-relative target atomically and
// durably: a new temporary file (forumfs.TempPrefix, created exclusively) in the
// target's directory is written and fsynced, then published, and the
// directory is fsynced. Without exclusive the temporary file is renamed
// over target. With exclusive it is hard-linked to target, which fails
// with os.ErrExist if target exists (even when created by a racing
// writer an instant earlier), and then removed. The target's directory
// must exist and no directory on the way may be a symbolic link.
func writeFileAt(r *os.Root, target string, data []byte, exclusive bool) error {
	name := filepath.Base(target)
	if err := noSymlinks(r, target); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	dir := filepath.Dir(target)
	tmpPath := filepath.Join(dir, forumfs.TempPrefix+uuid.NewString())
	tmp, err := r.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	// abandon discards the temporary file after a failure, keeping every
	// error so nothing is silently dropped.
	abandon := func(err error, closeFirst bool) error {
		if closeFirst {
			err = errors.Join(err, tmp.Close())
		}
		if rmErr := r.Remove(tmpPath); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			err = errors.Join(err, rmErr)
		}
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := tmp.Chmod(filePerm); err != nil {
		return abandon(err, true)
	}
	if _, err := tmp.Write(data); err != nil {
		return abandon(err, true)
	}
	if err := tmp.Sync(); err != nil {
		return abandon(err, true)
	}
	if err := tmp.Close(); err != nil {
		return abandon(err, false)
	}
	if exclusive {
		if err := r.Link(tmpPath, target); err != nil {
			return abandon(err, false)
		}
		if err := r.Remove(tmpPath); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	} else if err := r.Rename(tmpPath, target); err != nil {
		return abandon(err, false)
	}
	return syncDirAt(r, dir)
}

// syncDirAt fsyncs the r-relative directory dir.
func syncDirAt(r *os.Root, dir string) error {
	d, err := r.Open(dir)
	if err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dir, errors.Join(err, d.Close()))
	}
	return d.Close()
}

// syncDir fsyncs a directory so a rename or create inside it is durable.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // a directory under the forum base
	if err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dir, errors.Join(err, d.Close()))
	}
	return d.Close()
}
