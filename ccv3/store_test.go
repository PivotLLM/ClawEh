// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package ccv3

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The pinned clock the store tests use, and a later one for updates.
var (
	day1 = time.Unix(1_800_000_000, 0)
	day2 = time.Unix(1_800_086_400, 0)
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "cards"))
	if err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return day1 }
	return s
}

// dataOf decodes a card's data object.
func dataOf(t *testing.T, c *Card) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(c.Data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func wantRefusal(t *testing.T, err, kind error) {
	t.Helper()
	if !errors.Is(err, kind) {
		t.Fatalf("error = %v, want %v", err, kind)
	}
	var r interface{ Refusal() bool }
	if !errors.As(err, &r) || !r.Refusal() {
		t.Errorf("error %v is not a refusal", err)
	}
}

func TestOpenReturnsOneHandlePerDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cards")
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(dir + "/./")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Error("Open returned two handles for one directory")
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("directory mode = %o, want 700", info.Mode().Perm())
	}
	other, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if other == a {
		t.Error("Open returned one handle for two directories")
	}
	if _, err := Open(" "); err == nil {
		t.Error("Open(blank) succeeded")
	}
}

func TestCreateWritesEnvelopeDatesAndArrays(t *testing.T) {
	s := newStore(t)
	c, err := s.Create("Sherlock", json.RawMessage(`{"name":"Sherlock Holmes","creation_date":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Spec != SpecV3 || c.SpecVersion != SpecVersionV3 {
		t.Errorf("envelope = %q %q", c.Spec, c.SpecVersion)
	}
	d := dataOf(t, c)
	for _, k := range []string{"alternate_greetings", "group_only_greetings", "tags"} {
		if v, ok := d[k].([]any); !ok || len(v) != 0 {
			t.Errorf("%s = %v, want []", k, d[k])
		}
	}
	if d["creation_date"] != float64(day1.Unix()) || d["modification_date"] != float64(day1.Unix()) {
		t.Errorf("dates = %v %v, want %d", d["creation_date"], d["modification_date"], day1.Unix())
	}
	path := filepath.Join(s.dir, "sherlock.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 600", info.Mode().Perm())
	}
	got, err := s.Get("SHERLOCK")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Data, c.Data) {
		t.Errorf("Get = %s, want %s", got.Data, c.Data)
	}
	assertNoTempFiles(t, s)
}

func TestCreateRefusals(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("alice", json.RawMessage(`{"name":"Alice"}`)); err != nil {
		t.Fatal(err)
	}
	_, err := s.Create("Alice", json.RawMessage(`{"name":"Alice"}`))
	wantRefusal(t, err, ErrExists)
	_, err = s.Create("bad id", json.RawMessage(`{"name":"Alice"}`))
	wantRefusal(t, err, ErrInvalidID)
	_, err = s.Create("bob", json.RawMessage(`{"name":"Bob","mood":"x"}`))
	if got := issues(t, err); !slices.Equal(got, []string{"mood is not a card field"}) {
		t.Errorf("issues = %q", got)
	}
	_, err = s.Create("bob", json.RawMessage(`{"name":"Bob","description":"`+strings.Repeat("x", MaxCardBytes)+`"}`))
	wantRefusal(t, err, ErrTooLarge)
	if s.Exists("bob") {
		t.Error("a refused create wrote a card")
	}
	_, err = s.Get("nobody")
	wantRefusal(t, err, ErrNotFound)
}

// An update merges the patch over data: member order is kept, a null
// removes a member, arrays are replaced whole, modification_date moves and
// creation_date stays.
func TestUpdateMergesPatch(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("bob", json.RawMessage(`{"name":"Bob","description":"d","personality":"p","tags":["a","b"]}`)); err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return day2 }
	c, err := s.Update("bob", json.RawMessage(`{"description":"new","personality":null,"tags":["c"],"scenario":"s"}`))
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, c.Data); err != nil {
		t.Fatal(err)
	}
	want := `{"name":"Bob","description":"new","tags":["c"],"alternate_greetings":[],"group_only_greetings":[],` +
		`"creation_date":1800000000,"modification_date":1800086400,"scenario":"s"}`
	if compact.String() != want {
		t.Errorf("data = %s\nwant   %s", compact.String(), want)
	}
	assertNoTempFiles(t, s)
}

func TestUpdateRefusals(t *testing.T) {
	s := newStore(t)
	created, err := s.Create("bob", json.RawMessage(`{"name":"Bob"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, patch string
		want        []string
	}{
		{"not an object", `["x"]`, []string{"changes must be an object"}},
		{"dates", `{"creation_date":1,"modification_date":null}`, []string{
			"creation_date is set automatically; leave it out",
			"modification_date is set automatically; leave it out",
		}},
		{"invalid result", `{"name":null,"tags":"x"}`, []string{"name is required", "tags must be a list of text"}},
		{"unknown member", `{"mood":"x"}`, []string{"mood is not a card field"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, updateErr := s.Update("bob", json.RawMessage(tc.patch))
			if got := issues(t, updateErr); !slices.Equal(got, tc.want) {
				t.Errorf("issues = %q, want %q", got, tc.want)
			}
		})
	}
	got, err := s.Get("bob")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Data, created.Data) {
		t.Errorf("a refused update changed the card: %s", got.Data)
	}
	_, err = s.Update("nobody", json.RawMessage(`{}`))
	wantRefusal(t, err, ErrNotFound)
	_, err = s.Update("-x", json.RawMessage(`{}`))
	wantRefusal(t, err, ErrInvalidID)
}

// An unknown member an import kept survives updates; a new one is refused.
func TestUpdateKeepsImportedUnknownMembers(t *testing.T) {
	s := newStore(t)
	if _, _, err := s.Import("bob", []byte(`{"spec":"chara_card_v3","spec_version":"3.0","data":{"name":"Bob","mood":"x"}}`), false); err != nil {
		t.Fatal(err)
	}
	c, err := s.Update("bob", json.RawMessage(`{"description":"d"}`))
	if err != nil {
		t.Fatalf("Update = %v", err)
	}
	if dataOf(t, c)["mood"] != "x" {
		t.Error("update dropped the imported member")
	}
	_, err = s.Update("bob", json.RawMessage(`{"weight":1}`))
	if got := issues(t, err); !slices.Equal(got, []string{"weight is not a card field"}) {
		t.Errorf("issues = %q", got)
	}
}

func TestRenameCopyDelete(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("alice", json.RawMessage(`{"name":"Alice","description":"d"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("bob", json.RawMessage(`{"name":"Bob"}`)); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, s.Rename("alice", "Bob"), ErrExists)
	wantRefusal(t, s.Rename("nobody", "carol"), ErrNotFound)
	wantRefusal(t, s.Rename("alice", "bad id"), ErrInvalidID)
	if err := s.Rename("alice", "ALICE"); err != nil {
		t.Errorf("Rename to the same id = %v", err)
	}
	if err := s.Rename("Alice", "Carol"); err != nil {
		t.Fatal(err)
	}
	if s.Exists("alice") || !s.Exists("carol") {
		t.Error("rename did not move the card")
	}

	s.Now = func() time.Time { return day2 }
	wantRefusal(t, s.Copy("carol", "bob"), ErrExists)
	wantRefusal(t, s.Copy("nobody", "dave"), ErrNotFound)
	if err := s.Copy("carol", "dave"); err != nil {
		t.Fatal(err)
	}
	dave, err := s.Get("dave")
	if err != nil {
		t.Fatal(err)
	}
	d := dataOf(t, dave)
	if d["name"] != "Alice" || d["description"] != "d" {
		t.Errorf("copy data = %v", d)
	}
	if d["creation_date"] != float64(day2.Unix()) || d["modification_date"] != float64(day2.Unix()) {
		t.Errorf("copy dates = %v %v, want %d", d["creation_date"], d["modification_date"], day2.Unix())
	}

	wantRefusal(t, s.Delete("nobody"), ErrNotFound)
	wantRefusal(t, s.Delete("bad id"), ErrInvalidID)
	if err := s.Delete("Dave"); err != nil {
		t.Fatal(err)
	}
	if s.Exists("dave") {
		t.Error("delete left the card")
	}
	assertNoTempFiles(t, s)
}

// List reports cards by id and every foreign or invalid file with a reason.
func TestListReportsInvalidFiles(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("bob", json.RawMessage(`{"name":"Bob","nickname":"B","tags":["x"],"creator":"Alice"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("alice", json.RawMessage(`{"name":"Alice"}`)); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(s.dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("notes.txt", "x")
	write("Carol.json", `{"spec":"chara_card_v3","spec_version":"3.0","data":{"name":"C"}}`)
	write("broken.json", "{")
	write("v2.json", `{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"C"}}`)
	write("bad.json", `{"spec":"chara_card_v3","spec_version":"3.0","data":{"name":""}}`)
	write("big.json", strings.Repeat(" ", MaxCardBytes+1))
	write(".tmp-123", "in progress")
	if err := os.Mkdir(filepath.Join(s.dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	cards, invalid, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	wantCards := []Summary{
		{ID: "alice", Name: "Alice", Tags: []string{}, ModificationDate: day1.Unix()},
		{ID: "bob", Name: "Bob", Nickname: "B", Tags: []string{"x"}, Creator: "Alice", ModificationDate: day1.Unix()},
	}
	if len(cards) != len(wantCards) {
		t.Fatalf("cards = %+v", cards)
	}
	for i := range cards {
		if cards[i].ID != wantCards[i].ID || cards[i].Name != wantCards[i].Name || cards[i].Nickname != wantCards[i].Nickname ||
			!slices.Equal(cards[i].Tags, wantCards[i].Tags) || cards[i].Creator != wantCards[i].Creator ||
			cards[i].ModificationDate != wantCards[i].ModificationDate {
			t.Errorf("cards[%d] = %+v, want %+v", i, cards[i], wantCards[i])
		}
	}
	reasons := map[string]string{}
	for _, inv := range invalid {
		reasons[inv.File] = inv.Reason
	}
	want := map[string]string{
		"notes.txt":   "is not a .json file",
		"Carol.json":  "the file name is not a card id in lower case",
		"broken.json": "the file is not valid JSON",
		"v2.json":     "the file is not a V3 character card",
		"bad.json":    "The card is not valid: name must not be blank.",
		"big.json":    "the file is larger than 1 MiB",
		"sub":         "is a folder",
	}
	if len(reasons) != len(want) {
		t.Errorf("invalid = %v", invalid)
	}
	for f, r := range want {
		if reasons[f] != r {
			t.Errorf("invalid[%s] = %q, want %q", f, reasons[f], r)
		}
	}
	_, err = s.Get("bad")
	var r interface{ Refusal() bool }
	if err == nil || !errors.As(err, &r) {
		t.Errorf("Get(damaged) = %v, want a refusal", err)
	}
}

func TestStoreRender(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("bob", json.RawMessage(`{"name":"Bob","description":"{{char}} is here."}`)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Render("bob")
	if err != nil {
		t.Fatal(err)
	}
	if want := "# Character\n\nYou are Bob.\n\n## Description\nBob is here."; got != want {
		t.Errorf("Render = %q, want %q", got, want)
	}
	// The file keeps "<" as written, so <START> reads as written by hand.
	if _, uerr := s.Update("bob", json.RawMessage(`{"mes_example":"<START>\n{{char}}: Hi."}`)); uerr != nil {
		t.Fatal(uerr)
	}
	raw, rerr := os.ReadFile(s.path("bob"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !bytes.Contains(raw, []byte("<START>")) || bytes.Contains(raw, []byte(`\u003c`)) {
		t.Errorf("stored file escapes HTML: %s", raw)
	}
	_, err = s.Render("nobody")
	wantRefusal(t, err, ErrNotFound)
}

// Concurrent writers through one handle never lose a card or leave a
// temporary file.
func TestConcurrentWrites(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("bob", json.RawMessage(`{"name":"Bob"}`)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			if _, err := s.Update("bob", json.RawMessage(`{"tags":["t`+string(rune('0'+i))+`"]}`)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if _, err := s.Get("bob"); err != nil {
		t.Error(err)
	}
	assertNoTempFiles(t, s)
}

func assertNoTempFiles(t *testing.T, s *Store) {
	t.Helper()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempPrefix) {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}

// A write that fails (the directory is not writable) leaves the card as it
// was and no temporary file.
func TestFailedWriteLeavesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	s := newStore(t)
	if _, err := s.Create("bob", json.RawMessage(`{"name":"Bob"}`)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(s.dir, 0o700); err != nil {
			t.Error(err)
		}
	})
	if _, err := s.Update("bob", json.RawMessage(`{"description":"d"}`)); err == nil {
		t.Fatal("Update into a read-only directory succeeded")
	}
	c, err := s.Get("bob")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := dataOf(t, c)["description"]; ok {
		t.Error("a failed write changed the card")
	}
	assertNoTempFiles(t, s)
}

// pngChunk encodes one PNG chunk.
func pngChunk(typ string, body []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	b = append(b, typ...)
	b = append(b, body...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(append([]byte(typ), body...)))
}

// pngFile builds a PNG with an IHDR, the given tEXt chunks (keyword, JSON)
// and an IEND.
func pngFile(texts ...[2]string) []byte {
	b := bytes.NewBuffer(append([]byte(nil), pngSignature...))
	b.Write(pngChunk("IHDR", make([]byte, 13)))
	b.Write(pngChunk("tEXt", []byte("Comment\x00not a card")))
	for _, tx := range texts {
		b.Write(pngChunk("tEXt", []byte(tx[0]+"\x00"+base64.StdEncoding.EncodeToString([]byte(tx[1])))))
	}
	b.Write(pngChunk("IDAT", []byte{1, 2, 3}))
	b.Write(pngChunk("IEND", nil))
	return b.Bytes()
}

const (
	v2Card = `{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Watson","description":"A doctor.",` +
		`"personality":"","scenario":"","first_mes":"","mes_example":"","creator_notes":"","system_prompt":"",` +
		`"post_history_instructions":"","alternate_greetings":[],"tags":["doctor"],"creator":"Alice",` +
		`"character_version":"","extensions":{"talkativeness":"0.5"},"x_custom":1}}`
	v3Card = `{"spec":"chara_card_v3","spec_version":"3.0","data":{"name":"Holmes","creation_date":1600000000,` +
		`"group_only_greetings":[],"alternate_greetings":[],"tags":[],"mood":"dark"}}`
)

// A V2 card becomes V3: the new envelope, group_only_greetings and the
// dates added, everything else (unknown members included) kept in order.
func TestImportConvertsV2(t *testing.T) {
	s := newStore(t)
	c, format, err := s.Import("Watson", []byte(v2Card), false)
	if err != nil {
		t.Fatal(err)
	}
	if format != FormatV2JSON {
		t.Errorf("format = %q", format)
	}
	if c.Spec != SpecV3 || c.SpecVersion != SpecVersionV3 {
		t.Errorf("envelope = %q %q", c.Spec, c.SpecVersion)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, c.Data); err != nil {
		t.Fatal(err)
	}
	want := `{"name":"Watson","description":"A doctor.","personality":"","scenario":"","first_mes":"","mes_example":"",` +
		`"creator_notes":"","system_prompt":"","post_history_instructions":"","alternate_greetings":[],"tags":["doctor"],` +
		`"creator":"Alice","character_version":"","extensions":{"talkativeness":"0.5"},"x_custom":1,` +
		`"group_only_greetings":[],"creation_date":1800000000,"modification_date":1800000000}`
	if compact.String() != want {
		t.Errorf("data = %s\nwant   %s", compact.String(), want)
	}
}

// A V3 card keeps its creation_date and unknown members; an existing id is
// refused unless replace is set.
func TestImportV3AndReplace(t *testing.T) {
	s := newStore(t)
	c, format, err := s.Import("holmes", []byte(v3Card), false)
	if err != nil {
		t.Fatal(err)
	}
	d := dataOf(t, c)
	if format != FormatV3JSON || d["creation_date"] != float64(1600000000) || d["mood"] != "dark" ||
		d["modification_date"] != float64(day1.Unix()) {
		t.Errorf("format %q, data %v", format, d)
	}
	_, _, err = s.Import("Holmes", []byte(v3Card), false)
	wantRefusal(t, err, ErrExists)
	if _, _, err = s.Import("Holmes", []byte(v2Card), true); err != nil {
		t.Fatalf("Import(replace) = %v", err)
	}
	got, err := s.Get("holmes")
	if err != nil {
		t.Fatal(err)
	}
	if dataOf(t, got)["name"] != "Watson" {
		t.Error("replace did not replace the card")
	}
}

// Known members an imported card sets to null are dropped (treated as
// absent), in data and in character_book; unknown null members are kept.
// Create still refuses a null.
func TestImportDropsNullMembers(t *testing.T) {
	s := newStore(t)
	raw := `{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Bob","nickname":null,"tags":null,` +
		`"character_book":{"name":null,"entries":[]},"x_custom":null}}`
	c, _, err := s.Import("bob", []byte(raw), false)
	if err != nil {
		t.Fatalf("Import = %v", err)
	}
	var compact bytes.Buffer
	if err = json.Compact(&compact, c.Data); err != nil {
		t.Fatal(err)
	}
	want := `{"name":"Bob","character_book":{"entries":[]},"x_custom":null,"alternate_greetings":[],` +
		`"group_only_greetings":[],"tags":[],"creation_date":1800000000,"modification_date":1800000000}`
	if compact.String() != want {
		t.Errorf("data = %s\nwant   %s", compact.String(), want)
	}
	_, err = s.Create("carol", json.RawMessage(`{"name":"Carol","nickname":null}`))
	if got := issues(t, err); !slices.Equal(got, []string{"nickname must be text"}) {
		t.Errorf("Create(null) issues = %q", got)
	}
}

func TestImportRefusals(t *testing.T) {
	s := newStore(t)
	for _, tc := range []struct {
		name string
		raw  []byte
		kind error
		msg  string
	}{
		{"v1", []byte(`{"name":"Bob","description":"x"}`), nil, "This is a V1 card; convert it to V2 or V3 first."},
		{"not JSON", []byte(`hello`), nil, "The file is not a character card."},
		{"other spec", []byte(`{"spec":"chara_card_v9","data":{}}`), nil, `The card's spec "chara_card_v9" is not supported; use a V2 or V3 card.`},
		{"no data", []byte(`{"spec":"chara_card_v3"}`), nil, "The card has no data."},
		{"data not object", []byte(`{"spec":"chara_card_v3","data":[]}`), nil, "The card's data must be an object."},
		{"invalid card", []byte(`{"spec":"chara_card_v3","data":{"name":""}}`), nil, "The card is not valid: name must not be blank."},
		{"raw too large", make([]byte, MaxImportBytes+1), ErrTooLarge, "The file is larger than 8 MiB."},
		{"card too large", []byte(`{"spec":"chara_card_v3","data":{"name":"B","description":"` + strings.Repeat("x", MaxCardBytes) + `"}}`), ErrTooLarge, "The card is larger than 1 MiB."},
		{"png card too large", pngFile([2]string{"ccv3", `{"spec":"chara_card_v3","data":{"name":"B","description":"` + strings.Repeat("x", MaxCardBytes) + `"}}`}), ErrTooLarge, "The card is larger than 1 MiB."},
		{"png without card", pngFile(), nil, "The PNG file has no character card."},
		{"png truncated", pngFile([2]string{"ccv3", v3Card})[:60], nil, "The PNG file is damaged."},
		{"png without IEND", bytes.TrimSuffix(pngFile([2]string{"ccv3", v3Card}), pngChunk("IEND", nil)), nil, "The PNG file is damaged."},
		{"png bad CRC", corruptLastText(pngFile([2]string{"ccv3", v3Card})), nil, "The PNG file is damaged."},
		{"png bad base64", pngWithText("chara", "!!!"), nil, "The PNG file's character card is not readable."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := s.Import("bob", tc.raw, false)
			if err == nil || err.Error() != tc.msg {
				t.Fatalf("Import = %v, want %q", err, tc.msg)
			}
			if tc.kind != nil && !errors.Is(err, tc.kind) {
				t.Errorf("error is not %v", tc.kind)
			}
			var r interface{ Refusal() bool }
			if !errors.As(err, &r) || !r.Refusal() {
				t.Error("not a refusal")
			}
		})
	}
	if s.Exists("bob") {
		t.Error("a refused import wrote a card")
	}
	_, _, err := s.Import("bad id", []byte(v3Card), false)
	wantRefusal(t, err, ErrInvalidID)
}

func pngWithText(keyword, text string) []byte {
	b := bytes.NewBuffer(append([]byte(nil), pngSignature...))
	b.Write(pngChunk("tEXt", []byte(keyword+"\x00"+text)))
	b.Write(pngChunk("IEND", nil))
	return b.Bytes()
}

// corruptLastText flips a byte inside the data of the PNG's last tEXt chunk.
func corruptLastText(png []byte) []byte {
	out := append([]byte(nil), png...)
	i := bytes.LastIndex(out, []byte("tEXt"))
	out[i+6] ^= 0xff
	return out
}

// A PNG's ccv3 chunk wins over its chara chunk; a chara chunk alone is
// read; the format follows the card's spec.
func TestImportPNG(t *testing.T) {
	s := newStore(t)
	c, format, err := s.Import("a", pngFile([2]string{"chara", v2Card}, [2]string{"ccv3", v3Card}), false)
	if err != nil {
		t.Fatal(err)
	}
	if format != FormatV3PNG || dataOf(t, c)["name"] != "Holmes" {
		t.Errorf("format %q, name %v", format, dataOf(t, c)["name"])
	}
	c, format, err = s.Import("b", pngFile([2]string{"chara", v2Card}), false)
	if err != nil {
		t.Fatal(err)
	}
	if format != FormatV2PNG || dataOf(t, c)["name"] != "Watson" {
		t.Errorf("format %q, name %v", format, dataOf(t, c)["name"])
	}
	if _, ok := dataOf(t, c)["group_only_greetings"]; !ok {
		t.Error("V2 PNG card not converted")
	}
}
