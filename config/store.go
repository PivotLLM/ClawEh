// ClawEh
// License: MIT

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// Store is the one in-memory configuration of a running process, backed by
// config.json. Readers take a snapshot with Current; writers change it through
// Update, which serialises every read-modify-write-save so two WebUI requests
// cannot lose each other's change, validates what would be written with the
// same checks LoadConfig applies, and swaps the snapshot only after the file
// is on disk. Reload re-reads the file when something else wrote it.
type Store struct {
	path string

	mu  sync.RWMutex
	cur *Config
}

// ValidationError is returned by Update when the mutated config would be
// refused by LoadConfig. Callers use errors.As to report it as a client
// error rather than a failure to save.
type ValidationError struct {
	Err error
}

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

// ErrUnchanged may be returned by an Update callback that found nothing to
// change. Update then returns nil without writing: a save that changes
// nothing would still touch the file's mtime and make the gateway's config
// watcher reload for no reason.
var ErrUnchanged = errors.New("config unchanged")

// NewStore loads path and returns a Store holding the result. A missing file
// loads as the defaults, as with LoadConfig.
func NewStore(path string) (*Store, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		return nil, err
	}
	return &Store{path: path, cur: cfg}, nil
}

// Path returns the config file the store reads and writes.
func (s *Store) Path() string { return s.path }

// Current returns the live configuration. The returned value is shared with
// every other caller and with the store: treat it as read-only, and go through
// Update to change it. Use Clone for a private copy.
func (s *Store) Current() *Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// Update applies fn to a private copy of the current config, validates the
// result, writes it to disk through SaveConfig and makes it current. Nothing
// changes if fn returns an error (ErrUnchanged included, which Update reports
// as success), validation fails or the write fails. Updates are serialised: a
// concurrent Update sees this one's result.
//
// A secret reference that fn leaves in a field as a literal ("env:NAME", as
// submitted through the WebUI) is resolved before the config becomes current,
// and remembered so the save writes the reference.
func (s *Store) Update(fn func(cfg *Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	next, err := s.cur.Clone()
	if err != nil {
		return fmt.Errorf("copy config: %w", err)
	}
	if err = fn(next); err != nil {
		if errors.Is(err, ErrUnchanged) {
			return nil
		}
		return err
	}
	// fn may have replaced the whole struct (PUT /api/config does); the
	// runtime-only fields belong to this process, not to the request.
	next.dataDir = s.cur.dataDir
	if next.secretRefs == nil {
		next.secretRefs = s.cur.secretRefs
	}

	resolved, err := resolveConfigSecrets(next)
	if err != nil {
		return &ValidationError{Err: err}
	}
	if err := resolved.validateListeners(); err != nil {
		return &ValidationError{Err: err}
	}
	if err := SaveConfig(s.path, resolved); err != nil {
		return err
	}
	s.cur = resolved
	return nil
}

// Reload re-reads the file and makes it current, returning the new config.
// On a load error the current config is kept and the error returned.
func (s *Store) Reload() (*Config, error) {
	cfg, err := LoadConfig(s.path)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cur = cfg
	s.mu.Unlock()
	return cfg, nil
}

// Clone returns a deep copy of c, including its runtime-only fields, so the
// copy can be mutated without touching the shared configuration.
func (c *Config) Clone() (*Config, error) {
	if c == nil {
		return nil, errors.New("nil config")
	}
	data, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	out := &Config{}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, err
	}
	out.dataDir = c.dataDir
	out.secretRefs = append([]secretRef(nil), c.secretRefs...)
	return out, nil
}
