// ClawEh
// License: MIT

package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/utils"
)

// FileName is the credentials file's name inside CLAW_HOME.
const FileName = "credentials.json"

// FileMode is the only permission the gateway accepts on the credentials
// file: owner read/write, nothing for group or others.
const FileMode fs.FileMode = 0o600

// MinPasswordLength is the shortest password `claw admin` accepts.
const MinPasswordLength = 12

// ErrNotConfigured means there is no credentials file: no admin account has
// been created with `claw admin`.
var ErrNotConfigured = errors.New("no admin account")

// PermissionError reports a credentials file that other accounts on the host
// can read. The gateway refuses to use such a file; Fix is the command that
// repairs it.
type PermissionError struct {
	Path string
	Mode fs.FileMode
}

func (e *PermissionError) Error() string {
	return fmt.Sprintf("credentials file %s has mode %04o; it must be 0600 (run: %s)", e.Path, e.Mode, e.Fix())
}

// Fix is the shell command that repairs the permissions.
func (e *PermissionError) Fix() string { return "chmod 600 " + e.Path }

// Credentials is the content of credentials.json: one admin account.
type Credentials struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	Updated      time.Time `json:"updated"`
}

// Path returns the credentials file path for a CLAW_HOME.
func Path(clawHome string) string {
	return filepath.Join(clawHome, FileName)
}

// Load reads and validates the credentials file at path. It returns
// ErrNotConfigured when the file does not exist, a *PermissionError when the
// file is group- or world-accessible, and ErrInvalidHash (wrapped) when the
// stored hash is malformed. It never logs; the caller decides how loud to be.
func Load(path string) (*Credentials, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotConfigured
		}
		return nil, fmt.Errorf("stat credentials file: %w", err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("credentials path %s is a directory", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, &PermissionError{Path: path, Mode: fi.Mode().Perm()}
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is <CLAW_HOME>/credentials.json, chosen by the operator
	if err != nil {
		return nil, fmt.Errorf("read credentials file: %w", err)
	}
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse credentials file %s: %w", path, err)
	}
	if strings.TrimSpace(c.Username) == "" {
		return nil, fmt.Errorf("credentials file %s: username is empty", path)
	}
	if err := ParseHash(c.PasswordHash); err != nil {
		return nil, fmt.Errorf("credentials file %s: %w", path, err)
	}
	return &c, nil
}

// Write hashes password and writes the credentials file atomically with mode
// 0600, replacing any existing account. The directory is created if missing.
func Write(path, username, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(Credentials{
		Username:     username,
		PasswordHash: hash,
		Updated:      time.Now().UTC().Truncate(time.Second),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return fmt.Errorf("create %s: %w", dir, mkErr)
	}
	tmp, err := os.CreateTemp(dir, ".credentials-*.json")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		if rmErr := os.Remove(tmpName); rmErr != nil {
			logger.WarnCF("admin", "could not remove temp credentials file", map[string]any{"path": tmpName, "error": rmErr.Error()})
		}
	}
	// CreateTemp uses 0600 already; make it explicit so a umask cannot widen it.
	if err := tmp.Chmod(FileMode); err != nil {
		utils.CloseQuietly(tmp)
		cleanup()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		utils.CloseQuietly(tmp)
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		utils.CloseQuietly(tmp)
		cleanup()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// Verify reports whether username and password match the account. The
// username comparison is constant time over SHA-256 digests, and the password
// is always hashed, so a wrong username costs the same as a wrong password.
func (c *Credentials) Verify(username, password string) bool {
	want := sha256.Sum256([]byte(c.Username))
	got := sha256.Sum256([]byte(username))
	userOK := subtle.ConstantTimeCompare(want[:], got[:])

	passOK, err := VerifyPassword(c.PasswordHash, password)
	if err != nil || !passOK {
		return false
	}
	return userOK == 1
}
