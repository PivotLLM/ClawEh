// ClawEh
// License: MIT

package fileutil

import (
	"os"
	"path/filepath"
)

// MkdirAllExact creates path and any missing parents, giving each folder it
// creates exactly mode whatever the umask. Folders that already exist keep
// their mode.
func MkdirAllExact(path string, mode os.FileMode) error {
	return MkdirAllWith(path, func(string) os.FileMode { return mode })
}

// MkdirAllWith is MkdirAllExact with each folder's mode chosen by modeFor
// (given the folder's path), for a tree whose folders lie in different
// areas.
func MkdirAllWith(path string, modeFor func(dir string) os.FileMode) error {
	path = filepath.Clean(path)
	if fi, err := os.Stat(path); err == nil {
		if fi.IsDir() {
			return nil
		}
		return &os.PathError{Op: "mkdir", Path: path, Err: os.ErrExist}
	}
	if parent := filepath.Dir(path); parent != path {
		if err := MkdirAllWith(parent, modeFor); err != nil {
			return err
		}
	}
	mode := modeFor(path).Perm()
	if err := os.Mkdir(path, mode); err != nil {
		if os.IsExist(err) {
			return nil // created concurrently
		}
		return err
	}
	return os.Chmod(path, mode)
}

// WriteFileExact writes data to path like os.WriteFile and leaves it with
// exactly mode whatever the umask (an existing file is set to mode too). The
// parent folder must exist.
func WriteFileExact(path string, data []byte, mode os.FileMode) error {
	if err := os.WriteFile(path, data, mode.Perm()); err != nil {
		return err
	}
	return os.Chmod(path, mode.Perm())
}
