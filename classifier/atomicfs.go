// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/PivotLLM/ClawEh/classifier/classifierfs"
)

// mkdirAllExact creates dir and any missing parents, each with exactly mode,
// whatever the umask; folders that exist keep theirs.
func mkdirAllExact(dir string, mode os.FileMode) error {
	dir = filepath.Clean(dir)
	if fi, err := os.Stat(dir); err == nil { // #nosec G703 -- a scope's base directory, configured by the host
		if fi.IsDir() {
			return nil
		}
		return &os.PathError{Op: "mkdir", Path: dir, Err: os.ErrExist}
	}
	if parent := filepath.Dir(dir); parent != dir {
		if err := mkdirAllExact(parent, mode); err != nil {
			return err
		}
	}
	if err := os.Mkdir(dir, mode); err != nil { // #nosec G703 -- as above
		if errors.Is(err, fs.ErrExist) {
			return nil
		}
		return err
	}
	return os.Chmod(dir, mode) // #nosec G703 -- as above
}

// writeAtomic writes data to path atomically and durably with exactly mode:
// a new temporary file (classifierfs.TempPrefix, created exclusively) beside
// it is written and fsynced, then published, and the folder is fsynced.
// Without exclusive the temporary file is renamed over path; with exclusive it
// is hard-linked to path, which fails with fs.ErrExist when path exists, and
// then removed. The folder must exist.
func writeAtomic(path string, data []byte, mode os.FileMode, exclusive bool) error {
	dir := filepath.Dir(path)
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmpPath := filepath.Join(dir, classifierfs.TempPrefix+hex.EncodeToString(rnd[:]))
	tmp, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) // #nosec G304 -- a new file in the scope's base directory
	if err != nil {
		return err
	}
	abandon := func(err error, closeFirst bool) error {
		if closeFirst {
			err = errors.Join(err, tmp.Close())
		}
		if rmErr := os.Remove(tmpPath); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			err = errors.Join(err, rmErr)
		}
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
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
		if err := os.Link(tmpPath, path); err != nil {
			return abandon(err, false)
		}
		if err := os.Remove(tmpPath); err != nil {
			return err
		}
	} else if err := os.Rename(tmpPath, path); err != nil {
		return abandon(err, false)
	}
	return syncDir(dir)
}

// syncDir fsyncs a folder so a rename or create inside it is durable.
func syncDir(dir string) error {
	d, err := os.Open(dir) // #nosec G304 -- the scope's base directory
	if err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dir, errors.Join(err, d.Close()))
	}
	return d.Close()
}

// marshalDefinition is the JSON a definition file holds: indented,
// newline-terminated and without HTML escaping, so it reads well by hand.
func marshalDefinition(def *Definition) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(def); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
