// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package ccv3

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/internal/mergepatch"
)

const (
	dirPerm    = 0o700
	cardExt    = ".json"
	tempPrefix = ".tmp-"
)

// Store is the card library in one directory: one file <id>.json per card,
// with the id in lower case. Writes are atomic (a temporary file in the same
// directory, then a rename) and serialized by the store's mutex; reads take
// no lock, since a reader only ever sees a whole file.
type Store struct {
	// Now is the clock for the card dates; nil means time.Now. Tests set it
	// before using the store.
	Now func() time.Time

	dir string
	mu  sync.Mutex
}

// The one handle per directory, so every user in the process shares its
// write lock.
var (
	storesMu sync.Mutex
	stores   = map[string]*Store{}
)

// Open returns the process-wide store for dir, creating the directory (0700)
// when it is missing. Every call for the same directory returns the same
// *Store.
func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("open card store: no directory")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("open card store %s: %w", dir, err)
	}
	storesMu.Lock()
	defer storesMu.Unlock()
	if s, ok := stores[abs]; ok {
		return s, nil
	}
	if err := os.MkdirAll(abs, dirPerm); err != nil {
		return nil, fmt.Errorf("open card store %s: %w", abs, err)
	}
	s := &Store{dir: abs}
	stores[abs] = s
	return s, nil
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, NormalizeID(id)+cardExt)
}

func checkID(id string) error {
	if !ValidID(id) {
		return refuse(ErrInvalidID,
			"Card id %q is not valid; use 1 to 64 letters, digits, - and _, starting with a letter or digit.", id)
	}
	return nil
}

func notFound(id string) error { return refuse(ErrNotFound, "Card %q does not exist.", id) }

// List reports every card, sorted by id, and every other file in the
// directory as Invalid with the reason; nothing else is done with those.
func (s *Store) List() ([]Summary, []Invalid, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, nil, fmt.Errorf("list cards: %w", err)
	}
	cards := []Summary{}
	invalid := []Invalid{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, tempPrefix) {
			continue
		}
		stem, isJSON := strings.CutSuffix(name, cardExt)
		var reason string
		var card *Card
		switch {
		case e.IsDir():
			reason = "is a folder"
		case !isJSON:
			reason = "is not a .json file"
		case !ValidID(stem) || stem != NormalizeID(stem):
			reason = "the file name is not a card id in lower case"
		default:
			card, err = readCard(filepath.Join(s.dir, name))
			if err != nil {
				reason = err.Error()
			}
		}
		if reason != "" {
			invalid = append(invalid, Invalid{File: name, Reason: reason})
			continue
		}
		d, err := decodeData(card.Data)
		if err != nil {
			invalid = append(invalid, Invalid{File: name, Reason: err.Error()})
			continue
		}
		tags := d.Tags
		if tags == nil {
			tags = []string{}
		}
		cards = append(cards, Summary{
			ID: stem, Name: d.Name, Nickname: d.Nickname, Tags: tags,
			Creator: d.Creator, ModificationDate: d.ModificationDate,
		})
	}
	slices.SortFunc(cards, func(a, b Summary) int { return strings.Compare(a.ID, b.ID) })
	return cards, invalid, nil
}

// Exists reports whether id is a valid id with a card file.
func (s *Store) Exists(id string) bool {
	if !ValidID(id) {
		return false
	}
	info, err := os.Stat(s.path(id))
	return err == nil && info.Mode().IsRegular()
}

// Get returns the card as stored.
func (s *Store) Get(id string) (*Card, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	card, err := readCard(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, notFound(id)
	}
	var r interface{ Refusal() bool }
	if errors.As(err, &r) {
		return nil, refuse(nil, "Card %q is damaged: %s", id, err)
	}
	if err != nil {
		return nil, fmt.Errorf("read card %q: %w", id, err)
	}
	return card, nil
}

// Render returns the rendered prompt block of the card id (Render).
func (s *Store) Render(id string) (string, error) {
	card, err := s.Get(id)
	if err != nil {
		return "", err
	}
	return Render(card), nil
}

// Create writes a new card from its data object. Unknown members are
// refused; the dates are set (any given are replaced) and the required
// arrays are written as [] when absent.
func (s *Store) Create(id string, data json.RawMessage) (*Card, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	if err := Validate(data, false); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Exists(id) {
		return nil, refuse(ErrExists, "Card %q already exists.", id)
	}
	return s.write(id, data, stampAlways)
}

// Update applies an RFC 7386 merge patch to a card's data object (a null
// member removes a field; arrays and objects that are not merged member by
// member are replaced whole), validates the result as a whole and sets
// modification_date. The patch may not set either date, and may not add a
// member the spec does not define (one an import kept may stay). Nothing is
// written when the update is refused.
func (s *Store) Update(id string, patch json.RawMessage) (*Card, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	members, ok := object(patch)
	if !ok {
		return nil, &ValidationError{Issues: []Issue{{Path: "changes", Message: "must be an object"}}}
	}
	var issues []Issue
	for _, k := range []string{creationDate, modificationDate} {
		if _, ok := members[k]; ok {
			issues = append(issues, Issue{Path: k, Message: "is set automatically; leave it out"})
		}
	}
	if len(issues) > 0 {
		return nil, &ValidationError{Issues: issues}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	merged, err := mergepatch.Apply(current.Data, patch)
	if err != nil {
		return nil, refuse(nil, "The changes are not valid JSON: %s", err)
	}
	c := check(merged)
	kept := check(current.Data).unknown
	for _, p := range c.unknown {
		if !slices.Contains(kept, p) {
			c.add(p, "is not a card field")
		}
	}
	if len(c.issues) > 0 {
		return nil, &ValidationError{Issues: c.issues}
	}
	return s.write(id, merged, stampNever)
}

// Rename moves a card to a new id. Renaming to the same id in another case
// changes nothing.
func (s *Store) Rename(id, newID string) error {
	if err := checkID(id); err != nil {
		return err
	}
	if err := checkID(newID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.Exists(id) {
		return notFound(id)
	}
	if sameID(id, newID) {
		return nil
	}
	if s.Exists(newID) {
		return refuse(ErrExists, "Card %q already exists.", newID)
	}
	if err := os.Rename(s.path(id), s.path(newID)); err != nil {
		return fmt.Errorf("rename card %q to %q: %w", id, newID, err)
	}
	return nil
}

// Copy writes a new card newID with id's data and both dates set to now.
func (s *Store) Copy(id, newID string) error {
	if err := checkID(id); err != nil {
		return err
	}
	if err := checkID(newID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	source, err := s.Get(id)
	if err != nil {
		return err
	}
	if s.Exists(newID) {
		return refuse(ErrExists, "Card %q already exists.", newID)
	}
	_, err = s.write(newID, source.Data, stampAlways)
	return err
}

// Delete removes a card.
func (s *Store) Delete(id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.Exists(id) {
		return notFound(id)
	}
	if err := os.Remove(s.path(id)); err != nil {
		return fmt.Errorf("delete card %q: %w", id, err)
	}
	return nil
}

// Import stores a card from raw: a V2 or V3 envelope as JSON, or a PNG with
// the card in a ccv3 (preferred) or chara tEXt chunk. A V2 card is converted
// to V3; members the spec does not define are kept. creation_date is set
// when the card carries none, modification_date always. An existing id is
// refused unless replace is true. raw may be at most MaxImportBytes, the
// card JSON in it at most MaxCardBytes.
func (s *Store) Import(id string, raw []byte, replace bool) (*Card, Format, error) {
	if err := checkID(id); err != nil {
		return nil, "", err
	}
	data, format, err := parseImport(raw)
	if err != nil {
		return nil, "", err
	}
	if err = Validate(data, true); err != nil {
		return nil, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !replace && s.Exists(id) {
		return nil, "", refuse(ErrExists, "Card %q already exists.", id)
	}
	card, err := s.write(id, data, stampIfAbsent)
	if err != nil {
		return nil, "", err
	}
	return card, format, nil
}

// stampMode says when a write sets creation_date.
type stampMode int

const (
	stampNever stampMode = iota
	stampIfAbsent
	stampAlways
)

// write sets the dates and default arrays on data and writes the card
// atomically. The caller holds s.mu and has validated data.
func (s *Store) write(id string, data json.RawMessage, mode stampMode) (*Card, error) {
	members, _ := object(data)
	now := strconv.FormatInt(s.now().Unix(), 10)
	var patch bytes.Buffer
	patch.WriteByte('{')
	add := func(key, value string) {
		if patch.Len() > 1 {
			patch.WriteByte(',')
		}
		patch.WriteString(strconv.Quote(key) + ":" + value)
	}
	for _, k := range defaultArrays {
		if _, ok := members[k]; !ok {
			add(k, "[]")
		}
	}
	if _, has := members[creationDate]; mode == stampAlways || (mode == stampIfAbsent && !has) {
		add(creationDate, now)
	}
	add(modificationDate, now)
	patch.WriteByte('}')
	stamped, err := mergepatch.Apply(data, patch.Bytes())
	if err != nil {
		return nil, fmt.Errorf("stamp card %q: %w", id, err)
	}
	card := &Card{Spec: SpecV3, SpecVersion: SpecVersionV3, Data: stamped}
	out, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode card %q: %w", id, err)
	}
	if len(out) > MaxCardBytes {
		return nil, refuse(ErrTooLarge, "The card is larger than %d MiB.", MaxCardBytes>>20)
	}
	if err := atomicWrite(s.dir, s.path(id), append(out, '\n')); err != nil {
		return nil, fmt.Errorf("write card %q: %w", id, err)
	}
	return readCard(s.path(id))
}

// atomicWrite writes data to path through a temporary file in dir.
// os.CreateTemp creates the file 0600, so the card is never
// readable by others, not even while it is written.
func atomicWrite(dir, path string, data []byte) error {
	f, err := os.CreateTemp(dir, tempPrefix+"*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		if removeErr := os.Remove(tmp); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}
	return err
}

// readCard reads and checks a stored card. Members an import kept are
// allowed.
func readCard(path string) (*Card, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxCardBytes {
		return nil, refuse(ErrTooLarge, "the file is larger than %d MiB", MaxCardBytes>>20)
	}
	b, err := os.ReadFile(path) //nolint:gosec // a card file in the store's directory
	if err != nil {
		return nil, err
	}
	var card Card
	if err := json.Unmarshal(b, &card); err != nil {
		return nil, refuse(nil, "the file is not valid JSON")
	}
	if card.Spec != SpecV3 {
		return nil, refuse(nil, "the file is not a V3 character card")
	}
	if err := Validate(card.Data, true); err != nil {
		return nil, err
	}
	return &card, nil
}
