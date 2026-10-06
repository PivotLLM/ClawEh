// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// Drafts (DESIGN.md §7.21). A forum starts as a draft: a directory under the
// caller's base holding draft.json, whose configuration the agent sets up
// step by step (a template, an import, merge patches) without it having to
// be valid. Launch validates the draft and turns the same directory, under
// the same ID, into a running forum; only drafts can be changed. A launched
// forum's configuration is exported to start a new draft from it.

// Refusals of a forum that is no longer a draft, completing "forum <id> ...".
const (
	launchedNoEdit = "has already been launched; export its config into a new forum"
	launchedOnce   = "has already been launched"
)

// NewDraft creates an empty draft (configuration {}) owned by
// scope.AgentID and returns its ID.
//
// The forum's lock is held from before its directory exists until
// draft.json is written, so Recover never removes the half-created
// directory (ListIncomplete) from under it.
func (s *Service) NewDraft(_ context.Context, scope Scope) (string, error) {
	if !filepath.IsAbs(scope.BaseDirectory) {
		return "", fmt.Errorf("new draft: base directory %q is not absolute", scope.BaseDirectory)
	}
	id := uuid.NewString()
	guard := &Store{base: scope.BaseDirectory, id: id, root: filepath.Join(scope.BaseDirectory, id)}
	if err := guard.Lock(); err != nil {
		return "", err
	}
	defer guard.Unlock()
	store, err := CreateStore(scope.BaseDirectory, id)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	if err := store.WriteDraft(&Draft{Owner: scope.AgentID, CreatedAt: now, UpdatedAt: now, Config: json.RawMessage(`{}`)}); err != nil {
		return "", errors.Join(err, guard.Remove())
	}
	s.host.Logger.Infof("forum %s: draft created by agent %s", store.ID(), scope.AgentID)
	return store.ID(), nil
}

// SetDraftConfig replaces a draft's configuration with raw, which must be
// a JSON object (a template, or a configuration exported from a forum).
func (s *Service) SetDraftConfig(_ context.Context, scope Scope, id string, raw []byte) error {
	if !isJSONObject(raw) {
		return argIssue("the configuration must be a JSON object")
	}
	return s.editDraft(scope, id, func(json.RawMessage) (json.RawMessage, error) { return raw, nil })
}

// UpdateDraft applies patch, an RFC 7386 JSON merge patch that must be a
// JSON object, to a draft's configuration.
func (s *Service) UpdateDraft(_ context.Context, scope Scope, id string, patch []byte) error {
	if _, isObject, err := objectMembers(patch); err != nil || !isObject {
		return argIssue("the changes must be a JSON object")
	}
	return s.editDraft(scope, id, func(current json.RawMessage) (json.RawMessage, error) {
		return mergePatch(current, patch)
	})
}

// editDraft replaces a draft's configuration with what edit makes of it.
// It is serialised with the forum's other control operations and holds
// the forum's lock while it writes (ErrLocked when another process has it,
// a launch in progress there included). A forum that is not a draft is
// refused.
func (s *Service) editDraft(scope Scope, id string, edit func(json.RawMessage) (json.RawMessage, error)) error {
	defer s.control(id)()
	store, err := s.openDraft(scope, id, launchedNoEdit)
	if err != nil {
		return err
	}
	if err = store.Lock(); err != nil {
		return err
	}
	defer store.Unlock()
	if !store.IsDraft() {
		return invalidState("forum %s "+launchedNoEdit, id)
	}
	d, err := store.ReadDraft()
	if err != nil {
		return err
	}
	if d.Config, err = edit(d.Config); err != nil {
		return err
	}
	d.UpdatedAt = time.Now().UTC()
	return store.WriteDraft(d)
}

// ExportConfig returns the configuration of any of the scope's forums,
// indented: a draft's as it stands, a launched forum's as it was launched.
func (s *Service) ExportConfig(_ context.Context, scope Scope, id string) ([]byte, error) {
	store, err := s.open(scope, id)
	if err != nil {
		return nil, err
	}
	var raw []byte
	if store.IsDraft() {
		d, err := store.ReadDraft()
		if err != nil {
			return nil, err
		}
		raw = d.Config
	} else {
		snap, err := store.ReadSnapshot()
		if err != nil {
			return nil, corrupt("%s: %v", fileSnapshot, err)
		}
		if ownerErr := checkOwner(store, snap); ownerErr != nil {
			return nil, ownerErr
		}
		if raw, err = store.ReadConfig(); err != nil {
			return nil, corrupt("%s: %v", fileConfig, err)
		}
	}
	return formatConfig(raw)
}

// ValidateDraft validates a draft's configuration (Validate) without
// creating anything.
func (s *Service) ValidateDraft(ctx context.Context, id string, opts LaunchOptions) error {
	store, err := s.openDraft(opts.Scope, id, launchedOnce)
	if err != nil {
		return err
	}
	d, err := store.ReadDraft()
	if err != nil {
		return err
	}
	return s.Validate(ctx, d.Config, opts)
}

// openDraft opens a forum of the scope that must be a draft; any other is
// refused with "forum <id> <refusal>".
func (s *Service) openDraft(scope Scope, id, refusal string) (*Store, error) {
	store, err := s.open(scope, id)
	if err != nil {
		return nil, err
	}
	if !store.IsDraft() {
		return nil, invalidState("forum %s "+refusal, id)
	}
	return store, nil
}

// refuseDraft refuses an operation that needs a launched forum.
func refuseDraft(store *Store) error {
	if store.IsDraft() {
		return invalidState("forum %s is a draft and has not been launched", store.ID())
	}
	return nil
}

// draftSummary is a draft's Summary: its name, if the configuration has
// one, status draft and when it was last changed.
func draftSummary(store *Store) (*Summary, error) {
	d, err := store.ReadDraft()
	if err != nil {
		return nil, err
	}
	var named struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(d.Config, &named) != nil {
		named.Name = ""
	}
	return &Summary{ForumID: store.ID(), Name: named.Name, Status: StatusDraft, UpdatedAt: d.UpdatedAt, Layers: []LayerProgress{}}, nil
}

// revertLaunch makes a draft whose launch did not finish a draft again:
// the temporary agents created so far are deleted (those that could not be
// stay in the agents marker, which the next launch extends, and a failure
// is logged), the notice marker cleared and the directory reset to
// draft.json. On a draft with nothing to undo it changes nothing. The
// caller holds the lock. It runs even if the launching call was cancelled.
func (s *Service) revertLaunch(ctx context.Context, store *Store) error {
	if err := s.deleteTempAgents(context.WithoutCancel(ctx), store); err != nil {
		s.host.Logger.Warnf("forum %s: undoing a failed launch: %v (the registry's idle TTL removes them)", store.ID(), err)
	}
	if err := store.ClearCleanup(cleanupNotice); err != nil {
		return err
	}
	return store.ResetToDraft()
}

// formatConfig indents a configuration document for forum.json and export.
func formatConfig(raw []byte) ([]byte, error) {
	var b bytes.Buffer
	if err := json.Indent(&b, raw, "", "  "); err != nil {
		return nil, fmt.Errorf("format configuration: %w", err)
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}
