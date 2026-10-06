// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// routerFixture is a forum with two sources and three producing layers:
//
//   - review: alice, bob, editor; json output whose published projection
//     drops "private";
//   - notes: alice, bob; text output;
//   - skipped: disabled.
//
// Consuming layers are added per test with routerConsumer.
type routerFixture struct {
	cfg      *Config
	snap     *Snapshot
	files    map[string]string
	produced map[string][]OutputRecord
}

func routerNewFixture(t *testing.T) *routerFixture {
	t.Helper()
	disabled := false
	f := &routerFixture{
		cfg: &Config{
			Version: ConfigVersion,
			Participants: map[string]Participant{
				"alice":  {Agent: "alice", Name: "Alice"},
				"bob":    {Clone: "bob"},
				"editor": {Model: "m"},
				"chair":  {Model: "m"},
			},
			Layers: []Layer{
				{ID: "review", Participants: []string{"alice", "bob", "editor"}, Output: Output{Format: FormatJSON}},
				{ID: "notes", Participants: []string{"alice", "bob"}, Output: Output{Format: FormatText}},
				{ID: "skipped", Enabled: &disabled, Participants: []string{"alice"}, Output: Output{Format: FormatText}},
			},
		},
		snap: &Snapshot{
			Seed:   42,
			Layers: []string{"review", "notes"},
			Sources: map[string]SourceRecord{
				"spec":  {Decode: FormatJSON, File: "sources/spec.json"},
				"brief": {Decode: FormatText, File: "sources/brief.txt"},
			},
		},
		files: map[string]string{
			"sources/spec.json": `{"title":"T","body":{"text":"B","hidden":"H"}}`,
			"sources/brief.txt": "plain text brief",
		},
		produced: map[string][]OutputRecord{},
	}
	return f
}

// routerAddOutput records a committed output of a producing layer. For a
// json layer the full output is {"verdict":<text>,"round":<round>,
// "private":"p-<author>"} and the published one drops "private".
func (f *routerFixture) routerAddOutput(layerID string, round int, author string) OutputRecord {
	layer, _ := f.cfg.Layer(layerID)
	turn := TurnID(round, author)
	dir := fmt.Sprintf("layers/%s/calls/%s/1/", layerID, turn)
	rec := OutputRecord{
		OutputID:      layerID + "-" + turn,
		LayerID:       layerID,
		Round:         round,
		ParticipantID: author,
		Format:        layer.Output.Format,
		ContentFile:   dir + "output" + layer.Output.Format.Extension(),
		Turn:          turn,
		Attempt:       1,
	}
	if layer.Output.Format == FormatJSON {
		rec.PublishedFile = dir + "published.json"
		f.files[rec.ContentFile] = fmt.Sprintf(`{"verdict":"%s r%d","round":%d,"private":"p-%s"}`, author, round, round, author)
		f.files[rec.PublishedFile] = fmt.Sprintf(`{"verdict":"%s r%d","round":%d}`, author, round, round)
	} else {
		rec.PublishedFile = rec.ContentFile
		f.files[rec.ContentFile] = fmt.Sprintf("%s says r%d", author, round)
	}
	f.produced[layerID] = append(f.produced[layerID], rec)
	return rec
}

// routerConsumer appends an enabled consuming layer and returns it.
func (f *routerFixture) routerConsumer(id string, participants []string, routes ...Route) Layer {
	l := Layer{ID: id, Participants: participants, Inputs: routes, Output: Output{Format: FormatText}}
	f.cfg.Layers = append(f.cfg.Layers, l)
	f.snap.Layers = append(f.snap.Layers, id)
	return l
}

func (f *routerFixture) routerRead(rel string) ([]byte, error) {
	s, ok := f.files[rel]
	if !ok {
		return nil, fmt.Errorf("%s: %w", rel, fs.ErrNotExist)
	}
	return []byte(s), nil
}

func (f *routerFixture) routerResolve(layer Layer) (*LayerInputs, error) {
	return NewRouter(f.cfg, f.snap, f.routerRead).Resolve(layer, f.produced)
}

// routerIDs lists each item as "<source or output id>" for compact
// comparisons.
func routerIDs(items []InputItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		if it.Kind == InputSource {
			out = append(out, "source:"+it.SourceID)
		} else {
			out = append(out, it.OutputID)
		}
	}
	return out
}

// routerStandardOutputs gives review two rounds (round 2 committed out of
// order) and notes one round.
func (f *routerFixture) routerStandardOutputs() {
	f.routerAddOutput("review", 2, "bob")
	f.routerAddOutput("review", 1, "editor")
	f.routerAddOutput("review", 1, "alice")
	f.routerAddOutput("review", 1, "bob")
	f.routerAddOutput("review", 2, "alice")
	f.routerAddOutput("notes", 1, "bob")
	f.routerAddOutput("notes", 1, "alice")
}

func TestRouterSourceRoutes(t *testing.T) {
	tests := []struct {
		name        string
		route       Route
		wantContent string
		wantFormat  Format
		wantErr     string
	}{
		{name: "json source whole", route: Route{From: "source:spec"}, wantContent: `{"title":"T","body":{"text":"B","hidden":"H"}}`, wantFormat: FormatJSON},
		{name: "json source narrowed", route: Route{From: "source:spec", Paths: []string{"/body/text"}}, wantContent: `{"body":{"text":"B"}}`, wantFormat: FormatJSON},
		{name: "text source", route: Route{From: "source:brief"}, wantContent: "plain text brief", wantFormat: FormatText},
		{name: "paths on text source fail", route: Route{From: "source:brief", Paths: []string{"/a"}}, wantErr: "json content only"},
		{name: "missing path fails", route: Route{From: "source:spec", Paths: []string{"/nope"}}, wantErr: "selects nothing"},
		{name: "missing path fails even when optional", route: Route{From: "source:spec", Paths: []string{"/nope"}, Optional: true}, wantErr: "selects nothing"},
		{name: "unknown source", route: Route{From: "source:nope"}, wantErr: `unknown source "nope"`},
		{name: "same_participant on a source", route: Route{From: "source:spec", Distribute: DistributeSameParticipant}, wantErr: "layer routes only"},
		{name: "malformed from", route: Route{From: "spec"}, wantErr: "want"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := routerNewFixture(t)
			layer := f.routerConsumer("use", []string{"alice", "bob"}, tc.route)
			got, err := f.routerResolve(layer)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Resolve err = %v, want it to contain %q", err, tc.wantErr)
				}
				if !strings.Contains(err.Error(), "layer use input 0") {
					t.Errorf("error %q does not name the layer and route", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			for _, pid := range []string{"alice", "bob"} {
				items := got.Participants[pid]
				if len(items) != 1 {
					t.Fatalf("%s got %d items, want 1", pid, len(items))
				}
				it := items[0]
				if it.Kind != InputSource || it.Route != 0 || it.Content != tc.wantContent || it.Format != tc.wantFormat {
					t.Errorf("%s item = %+v, want source content %q format %s", pid, it, tc.wantContent, tc.wantFormat)
				}
			}
		})
	}
}

func TestRouterSourceReadError(t *testing.T) {
	f := routerNewFixture(t)
	delete(f.files, "sources/brief.txt")
	layer := f.routerConsumer("use", []string{"alice"}, Route{From: "source:brief", Optional: true})
	if _, err := f.routerResolve(layer); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Resolve err = %v, want fs.ErrNotExist (an optional route never excuses a read error)", err)
	}
}

func TestRouterLayerOrderingAndAttribution(t *testing.T) {
	f := routerNewFixture(t)
	f.routerStandardOutputs()
	layer := f.routerConsumer("use", []string{"alice"}, Route{From: "layer:review"})
	got, err := f.routerResolve(layer)
	if err != nil {
		t.Fatal(err)
	}
	items := got.Participants["alice"]
	want := []string{"review-r001-alice", "review-r001-bob", "review-r001-editor", "review-r002-alice", "review-r002-bob"}
	if ids := routerIDs(items); !reflect.DeepEqual(ids, want) {
		t.Fatalf("order = %v, want %v (round, then configured participant order)", ids, want)
	}
	first := items[0]
	if first.Kind != InputOutput || first.LayerID != "review" || first.Round != 1 || first.Author != "alice" ||
		first.AuthorName != "Alice" || first.Format != FormatJSON {
		t.Errorf("first item attribution = %+v", first)
	}
	if items[1].AuthorName != "bob" {
		t.Errorf("unnamed participant AuthorName = %q, want its ID", items[1].AuthorName)
	}
}

func TestRouterOrderOutputs(t *testing.T) {
	layer := Layer{Participants: []string{"bob", "alice"}}
	outs := []OutputRecord{
		{OutputID: "a2", Round: 2, ParticipantID: "alice"},
		{OutputID: "x1", Round: 1, ParticipantID: "stranger"},
		{OutputID: "a1", Round: 1, ParticipantID: "alice"},
		{OutputID: "b2", Round: 2, ParticipantID: "bob"},
		{OutputID: "b1", Round: 1, ParticipantID: "bob"},
	}
	orig := slices.Clone(outs)
	got := orderOutputs(layer, outs)
	ids := make([]string, 0, len(got))
	for _, o := range got {
		ids = append(ids, o.OutputID)
	}
	if want := []string{"b1", "a1", "x1", "b2", "a2"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("orderOutputs = %v, want %v", ids, want)
	}
	if !reflect.DeepEqual(outs, orig) {
		t.Error("orderOutputs modified its input")
	}
}

func TestRouterSelectAuthorsView(t *testing.T) {
	tests := []struct {
		name    string
		route   Route
		want    []string
		content map[string]string // output ID -> expected content
		wantErr string
	}{
		{
			name:  "select all is the default",
			route: Route{From: "layer:review"},
			want:  []string{"review-r001-alice", "review-r001-bob", "review-r001-editor", "review-r002-alice", "review-r002-bob"},
		},
		{
			name:  "last_per_participant keeps each author's newest across rounds",
			route: Route{From: "layer:review", Select: SelectLastPerParticipant},
			want:  []string{"review-r001-editor", "review-r002-alice", "review-r002-bob"},
		},
		{
			name:  "authors filter",
			route: Route{From: "layer:review", Authors: []string{"bob"}},
			want:  []string{"review-r001-bob", "review-r002-bob"},
		},
		{
			name:  "authors with last_per_participant",
			route: Route{From: "layer:review", Authors: []string{"editor", "alice"}, Select: SelectLastPerParticipant},
			want:  []string{"review-r001-editor", "review-r002-alice"},
		},
		{
			name:    "published view is the default",
			route:   Route{From: "layer:review", Authors: []string{"alice"}, Select: SelectLastPerParticipant},
			want:    []string{"review-r002-alice"},
			content: map[string]string{"review-r002-alice": `{"verdict":"alice r2","round":2}`},
		},
		{
			name:    "full view with explicit to",
			route:   Route{From: "layer:review", Authors: []string{"alice"}, Select: SelectLastPerParticipant, View: ViewFull, To: []string{"alice", "bob"}},
			want:    []string{"review-r002-alice"},
			content: map[string]string{"review-r002-alice": `{"verdict":"alice r2","round":2,"private":"p-alice"}`},
		},
		{
			name:    "paths on the published view",
			route:   Route{From: "layer:review", Authors: []string{"bob"}, Paths: []string{"/verdict"}},
			want:    []string{"review-r001-bob", "review-r002-bob"},
			content: map[string]string{"review-r001-bob": `{"verdict":"bob r1"}`},
		},
		{
			name:    "paths on the full view",
			route:   Route{From: "layer:review", Authors: []string{"bob"}, Paths: []string{"/private"}, View: ViewFull, To: []string{"alice", "bob"}},
			want:    []string{"review-r001-bob", "review-r002-bob"},
			content: map[string]string{"review-r002-bob": `{"private":"p-bob"}`},
		},
		{
			name:    "paths cannot widen the published view",
			route:   Route{From: "layer:review", Paths: []string{"/private"}},
			wantErr: `"/private" selects nothing`,
		},
		{
			name:    "paths cannot widen even when optional",
			route:   Route{From: "layer:review", Paths: []string{"/private"}, Optional: true},
			wantErr: "selects nothing",
		},
		{name: "full view without to fails", route: Route{From: "layer:review", View: ViewFull}, wantErr: "view full requires explicit to"},
		{name: "paths on a text producer fail", route: Route{From: "layer:notes", Paths: []string{"/a"}}, wantErr: "json content only"},
		{name: "unknown select", route: Route{From: "layer:review", Select: "best"}, wantErr: `unknown select "best"`},
		{name: "unknown view", route: Route{From: "layer:review", View: "partial"}, wantErr: `unknown view "partial"`},
		{name: "unknown distribute", route: Route{From: "layer:review", Distribute: "fair"}, wantErr: `unknown distribute "fair"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := routerNewFixture(t)
			f.routerStandardOutputs()
			layer := f.routerConsumer("use", []string{"alice", "bob"}, tc.route)
			got, err := f.routerResolve(layer)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Resolve err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			for _, pid := range []string{"alice", "bob"} {
				items := got.Participants[pid]
				if ids := routerIDs(items); !reflect.DeepEqual(ids, tc.want) {
					t.Fatalf("%s got %v, want %v", pid, ids, tc.want)
				}
				for _, it := range items {
					if want, ok := tc.content[it.OutputID]; ok && it.Content != want {
						t.Errorf("%s content of %s = %s, want %s", pid, it.OutputID, it.Content, want)
					}
				}
			}
		})
	}
}

func TestRouterRecipients(t *testing.T) {
	t.Run("to limits the recipients", func(t *testing.T) {
		f := routerNewFixture(t)
		layer := f.routerConsumer("use", []string{"alice", "bob"}, Route{From: "source:brief", To: []string{"bob"}})
		got, err := f.routerResolve(layer)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Participants["alice"]) != 0 || len(got.Participants["bob"]) != 1 {
			t.Errorf("participants = %+v, want only bob", got.Participants)
		}
	})
	t.Run("a layer with no participants fails", func(t *testing.T) {
		f := routerNewFixture(t)
		layer := f.routerConsumer("use", nil, Route{From: "source:brief", Distribute: DistributeRandom, Optional: true})
		if _, err := f.routerResolve(layer); err == nil || !strings.Contains(err.Error(), "no recipients") {
			t.Fatalf("Resolve err = %v, want a no-recipients error", err)
		}
	})
	t.Run("to naming a non-participant fails", func(t *testing.T) {
		f := routerNewFixture(t)
		layer := f.routerConsumer("use", []string{"alice"}, Route{From: "source:brief", To: []string{"chair"}})
		if _, err := f.routerResolve(layer); err == nil || !strings.Contains(err.Error(), `recipient "chair"`) {
			t.Fatalf("Resolve err = %v, want a recipient error", err)
		}
	})
}

func TestRouterProducerRules(t *testing.T) {
	tests := []struct {
		name        string
		route       Route
		wantMissing []int
		wantErr     string
	}{
		{name: "disabled producer, required", route: Route{From: "layer:skipped"}, wantErr: "disabled"},
		{name: "disabled producer, optional", route: Route{From: "layer:skipped", Optional: true}, wantMissing: []int{1}},
		{name: "empty selection, required", route: Route{From: "layer:review", Authors: []string{"chair"}}, wantErr: "selects nothing"},
		{name: "empty selection, optional", route: Route{From: "layer:review", Authors: []string{"chair"}, Optional: true}, wantMissing: []int{1}},
		{name: "unknown layer", route: Route{From: "layer:nope"}, wantErr: `unknown layer "nope"`},
		{name: "self reference", route: Route{From: "layer:use"}, wantErr: "backward only"},
		{name: "forward reference", route: Route{From: "layer:later"}, wantErr: "backward only"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := routerNewFixture(t)
			f.routerStandardOutputs()
			layer := f.routerConsumer("use", []string{"alice", "bob"}, Route{From: "source:brief"}, tc.route)
			f.routerConsumer("later", []string{"alice"})
			got, err := f.routerResolve(layer)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Resolve err = %v, want it to contain %q", err, tc.wantErr)
				}
				if !strings.Contains(err.Error(), "input 1") {
					t.Errorf("error %q does not name route 1", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if !reflect.DeepEqual(got.Missing, tc.wantMissing) {
				t.Errorf("Missing = %v, want %v", got.Missing, tc.wantMissing)
			}
			if ids := routerIDs(got.Participants["alice"]); !reflect.DeepEqual(ids, []string{"source:brief"}) {
				t.Errorf("alice got %v, want only the source", ids)
			}
		})
	}
}

func TestRouterSameParticipant(t *testing.T) {
	t.Run("each recipient gets its own records", func(t *testing.T) {
		f := routerNewFixture(t)
		f.routerStandardOutputs()
		layer := f.routerConsumer("use", []string{"alice", "bob"}, Route{From: "layer:review", Distribute: DistributeSameParticipant})
		got, err := f.routerResolve(layer)
		if err != nil {
			t.Fatal(err)
		}
		if ids := routerIDs(got.Participants["alice"]); !reflect.DeepEqual(ids, []string{"review-r001-alice", "review-r002-alice"}) {
			t.Errorf("alice got %v", ids)
		}
		if ids := routerIDs(got.Participants["bob"]); !reflect.DeepEqual(ids, []string{"review-r001-bob", "review-r002-bob"}) {
			t.Errorf("bob got %v", ids)
		}
	})
	t.Run("a recipient who authored nothing fails a required route", func(t *testing.T) {
		f := routerNewFixture(t)
		f.routerAddOutput("review", 1, "alice")
		layer := f.routerConsumer("use", []string{"alice", "bob"}, Route{From: "layer:review", Distribute: DistributeSameParticipant})
		if _, err := f.routerResolve(layer); err == nil || !strings.Contains(err.Error(), `recipient "bob" receives nothing`) {
			t.Fatalf("Resolve err = %v, want bob's empty bundle to fail", err)
		}
	})
	t.Run("a recipient who authored nothing is allowed on an optional route", func(t *testing.T) {
		f := routerNewFixture(t)
		f.routerAddOutput("review", 1, "alice")
		layer := f.routerConsumer("use", []string{"alice", "bob"}, Route{From: "layer:review", Distribute: DistributeSameParticipant, Optional: true})
		got, err := f.routerResolve(layer)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Participants["alice"]) != 1 || len(got.Participants["bob"]) != 0 || len(got.Missing) != 0 {
			t.Errorf("got %+v, want one item for alice, none for bob, nothing missing", got)
		}
	})
}

// routerRandomFixture has the given number of review outputs and a
// consumer dealing them at random to three recipients.
func routerRandomFixture(t *testing.T, seed int64, optional bool, outputs int) (*routerFixture, Layer) {
	t.Helper()
	f := routerNewFixture(t)
	f.snap.Seed = seed
	authors := []string{"alice", "bob", "editor"}
	for i := range outputs {
		f.routerAddOutput("review", i/len(authors)+1, authors[i%len(authors)])
	}
	layer := f.routerConsumer("use", []string{"alice", "bob", "editor"},
		Route{From: "layer:review", Distribute: DistributeRandom, Optional: optional})
	return f, layer
}

func TestRouterRandomDistribution(t *testing.T) {
	t.Run("each record once, counts differ by at most one", func(t *testing.T) {
		for _, n := range []int{3, 4, 5, 6} {
			f, layer := routerRandomFixture(t, 7, false, n)
			got, err := f.routerResolve(layer)
			if err != nil {
				t.Fatalf("%d records: %v", n, err)
			}
			seen := map[string]int{}
			minC, maxC := n, 0
			for _, pid := range layer.Participants {
				c := len(got.Participants[pid])
				minC, maxC = min(minC, c), max(maxC, c)
				for _, it := range got.Participants[pid] {
					seen[it.OutputID]++
				}
			}
			if len(seen) != n {
				t.Errorf("%d records: %d distinct delivered", n, len(seen))
			}
			for id, c := range seen {
				if c != 1 {
					t.Errorf("%d records: %s delivered %d times", n, id, c)
				}
			}
			if maxC-minC > 1 {
				t.Errorf("%d records: counts range %d..%d, want a difference of at most one", n, minC, maxC)
			}
		}
	})
	t.Run("same seed gives the same assignment", func(t *testing.T) {
		f1, l1 := routerRandomFixture(t, 99, false, 6)
		f2, l2 := routerRandomFixture(t, 99, false, 6)
		a, err := f1.routerResolve(l1)
		if err != nil {
			t.Fatal(err)
		}
		b, err := f2.routerResolve(l2)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Errorf("same seed, different assignments:\n%+v\n%+v", a.Participants, b.Participants)
		}
	})
	t.Run("the seed changes the assignment", func(t *testing.T) {
		f, l := routerRandomFixture(t, 1, false, 6)
		base, err := f.routerResolve(l)
		if err != nil {
			t.Fatal(err)
		}
		differs := false
		for seed := int64(2); seed < 20 && !differs; seed++ {
			f.snap.Seed = seed
			other, err := f.routerResolve(l)
			if err != nil {
				t.Fatal(err)
			}
			differs = !reflect.DeepEqual(base.Participants, other.Participants)
		}
		if !differs {
			t.Error("eighteen other seeds all gave the same assignment")
		}
	})
	t.Run("persisted assignment round-trips unchanged", func(t *testing.T) {
		f, l := routerRandomFixture(t, 5, false, 6)
		got, err := f.routerResolve(l)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var back LayerInputs
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(&back, got) {
			t.Errorf("inputs.json round trip changed the assignment:\n%+v\n%+v", got, &back)
		}
	})
	t.Run("fewer records than recipients requires optional", func(t *testing.T) {
		f, l := routerRandomFixture(t, 3, false, 2)
		if _, err := f.routerResolve(l); err == nil || !strings.Contains(err.Error(), "receives nothing") {
			t.Fatalf("Resolve err = %v, want an empty-bundle failure", err)
		}
		f, l = routerRandomFixture(t, 3, true, 2)
		got, err := f.routerResolve(l)
		if err != nil {
			t.Fatal(err)
		}
		// Round-robin in configured order: the first two recipients get one each.
		if len(got.Participants["alice"]) != 1 || len(got.Participants["bob"]) != 1 || len(got.Participants["editor"]) != 0 {
			t.Errorf("got %+v, want alice and bob one each, editor none", got.Participants)
		}
	})
	t.Run("random over a source deals the single record to the first recipient", func(t *testing.T) {
		f := routerNewFixture(t)
		layer := f.routerConsumer("use", []string{"bob", "alice"}, Route{From: "source:brief", Distribute: DistributeRandom, Optional: true})
		got, err := f.routerResolve(layer)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Participants["bob"]) != 1 || len(got.Participants["alice"]) != 0 {
			t.Errorf("got %+v, want bob to receive the source", got.Participants)
		}
	})
}

func TestRouterLayerRand(t *testing.T) {
	draw := func(seed int64, id string) []uint64 {
		r := layerRand(seed, id)
		return []uint64{r.Uint64(), r.Uint64(), r.Uint64()}
	}
	if !reflect.DeepEqual(draw(1, "use"), draw(1, "use")) {
		t.Error("layerRand is not deterministic")
	}
	if reflect.DeepEqual(draw(1, "use"), draw(1, "other")) {
		t.Error("two layers share one random stream")
	}
	if reflect.DeepEqual(draw(1, "use"), draw(2, "use")) {
		t.Error("the seed does not change the stream")
	}
}

func TestRouterConcatenationAndDedupe(t *testing.T) {
	f := routerNewFixture(t)
	f.routerStandardOutputs()
	layer := f.routerConsumer("use", []string{"alice", "bob"},
		Route{From: "layer:notes"},                                                           // 0
		Route{From: "source:brief"},                                                          // 1
		Route{From: "layer:review", Authors: []string{"alice"}},                              // 2
		Route{From: "source:brief"},                                                          // 3: identical source, dropped
		Route{From: "layer:review", Select: SelectLastPerParticipant},                        // 4: alice r2 duplicate dropped
		Route{From: "layer:review", Authors: []string{"alice"}, Paths: []string{"/verdict"}}, // 5: different projection kept
	)
	got, err := f.routerResolve(layer)
	if err != nil {
		t.Fatal(err)
	}
	items := got.Participants["alice"]
	wantIDs := []string{
		"notes-r001-alice", "notes-r001-bob",
		"source:brief",
		"review-r001-alice", "review-r002-alice",
		"review-r001-editor", "review-r002-bob",
		"review-r001-alice", "review-r002-alice",
	}
	if ids := routerIDs(items); !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("alice got %v, want %v", ids, wantIDs)
	}
	wantRoutes := []int{0, 0, 1, 2, 2, 4, 4, 5, 5}
	for i, it := range items {
		if it.Route != wantRoutes[i] {
			t.Errorf("item %d (%s) route = %d, want %d (first route wins)", i, routerIDs(items[i : i+1])[0], it.Route, wantRoutes[i])
		}
	}
	if items[7].Content != `{"verdict":"alice r1"}` {
		t.Errorf("projected duplicate content = %s", items[7].Content)
	}
	if !reflect.DeepEqual(got.Participants["alice"], got.Participants["bob"]) {
		t.Error("alice and bob should receive the same broadcast")
	}
}

func TestRouterDedupePerRecipient(t *testing.T) {
	// The same record reaching two recipients through different routes is
	// kept for each: deduplication is per recipient.
	f := routerNewFixture(t)
	layer := f.routerConsumer("use", []string{"alice", "bob"},
		Route{From: "source:brief", To: []string{"alice"}},
		Route{From: "source:brief", To: []string{"bob"}},
		Route{From: "source:brief"},
	)
	got, err := f.routerResolve(layer)
	if err != nil {
		t.Fatal(err)
	}
	if a, b := got.Participants["alice"], got.Participants["bob"]; len(a) != 1 || len(b) != 1 || a[0].Route != 0 || b[0].Route != 1 {
		t.Errorf("got alice %+v bob %+v, want one item each from routes 0 and 1", a, b)
	}
}

func TestRouterSpecExamples(t *testing.T) {
	t.Run("one-to-one", func(t *testing.T) {
		// rev 3 §4: filter authors, select last_per_participant, name one to.
		f := routerNewFixture(t)
		f.routerStandardOutputs()
		layer := f.routerConsumer("use", []string{"alice", "bob"},
			Route{From: "layer:review", Authors: []string{"alice"}, Select: SelectLastPerParticipant, To: []string{"bob"}})
		got, err := f.routerResolve(layer)
		if err != nil {
			t.Fatal(err)
		}
		if ids := routerIDs(got.Participants["bob"]); !reflect.DeepEqual(ids, []string{"review-r002-alice"}) {
			t.Errorf("bob got %v, want alice's last review", ids)
		}
		if len(got.Participants["alice"]) != 0 {
			t.Errorf("alice got %v, want nothing", routerIDs(got.Participants["alice"]))
		}
	})
	t.Run("whole-layer broadcast", func(t *testing.T) {
		f := routerNewFixture(t)
		f.routerStandardOutputs()
		layer := f.routerConsumer("use", []string{"alice", "bob", "editor"},
			Route{From: "layer:review", Select: SelectAll, Distribute: DistributeAll})
		got, err := f.routerResolve(layer)
		if err != nil {
			t.Fatal(err)
		}
		for _, pid := range layer.Participants {
			if n := len(got.Participants[pid]); n != 5 {
				t.Errorf("%s got %d records, want all 5", pid, n)
			}
		}
	})
	t.Run("specification example: report layer", func(t *testing.T) {
		// §7: report takes source:report, layer:review and an optional
		// layer:debate; with debate disabled the optional route is missing.
		f := routerNewFixture(t)
		f.routerStandardOutputs()
		layer := f.routerConsumer("report", []string{"editor"},
			Route{From: "source:spec"}, Route{From: "layer:review"}, Route{From: "layer:skipped", Optional: true})
		got, err := f.routerResolve(layer)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(got.Participants["editor"]); n != 6 {
			t.Errorf("editor got %d items, want the source and five reviews", n)
		}
		if !reflect.DeepEqual(got.Missing, []int{2}) {
			t.Errorf("Missing = %v, want [2]", got.Missing)
		}
	})
}

func TestRouterModerator(t *testing.T) {
	newLayer := func(f *routerFixture, routes ...Route) Layer {
		l := f.routerConsumer("use", []string{"alice", "bob"}, Route{From: "source:brief"})
		l.Moderator = &Moderator{Participant: "chair", Inputs: routes}
		f.cfg.Layers[len(f.cfg.Layers)-1] = l
		return l
	}
	t.Run("moderator receives its routes, deduplicated", func(t *testing.T) {
		f := routerNewFixture(t)
		f.routerStandardOutputs()
		layer := newLayer(f, Route{From: "source:spec"}, Route{From: "layer:review", Select: SelectLastPerParticipant},
			Route{From: "source:spec"}, Route{From: "layer:skipped", Optional: true})
		got, err := f.routerResolve(layer)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"source:spec", "review-r001-editor", "review-r002-alice", "review-r002-bob"}
		if ids := routerIDs(got.Moderator); !reflect.DeepEqual(ids, want) {
			t.Errorf("moderator got %v, want %v", ids, want)
		}
		if len(got.Missing) != 0 {
			t.Errorf("Missing = %v: moderator routes are not listed", got.Missing)
		}
		if _, ok := got.Participants["chair"]; ok {
			t.Error("the moderator appears among the participants")
		}
	})
	// The moderator is a moderator input's one explicit recipient, so view
	// full needs no `to` there (ValidateStatic agrees); the moderator sees
	// the private members a published view drops.
	t.Run("view full without to reaches the moderator", func(t *testing.T) {
		f := routerNewFixture(t)
		f.routerStandardOutputs()
		layer := newLayer(f, Route{From: "layer:review", View: ViewFull, Authors: []string{"alice"}})
		got, err := f.routerResolve(layer)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Moderator) == 0 {
			t.Fatal("the moderator received nothing")
		}
		for _, it := range got.Moderator {
			if !strings.Contains(it.Content, `"private":"p-alice"`) {
				t.Errorf("moderator item %s is not the full output: %s", it.OutputID, it.Content)
			}
		}
	})
	tests := []struct {
		name    string
		route   Route
		wantErr string
	}{
		{"to is rejected", Route{From: "source:spec", To: []string{"alice"}}, "takes no to"},
		{"empty required route fails", Route{From: "layer:review", Authors: []string{"chair"}}, "selects nothing"},
		{"same_participant leaves the moderator nothing", Route{From: "layer:review", Distribute: DistributeSameParticipant}, `recipient "chair" receives nothing`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := routerNewFixture(t)
			f.routerStandardOutputs()
			layer := newLayer(f, tc.route)
			_, err := f.routerResolve(layer)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), "moderator input 0") {
				t.Fatalf("Resolve err = %v, want a moderator input 0 error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestRouterOutputReadError(t *testing.T) {
	f := routerNewFixture(t)
	rec := f.routerAddOutput("review", 1, "alice")
	delete(f.files, rec.PublishedFile)
	layer := f.routerConsumer("use", []string{"alice"}, Route{From: "layer:review", Optional: true})
	if _, err := f.routerResolve(layer); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Resolve err = %v, want fs.ErrNotExist", err)
	}
}

func TestRouterDedupe(t *testing.T) {
	items := []InputItem{
		{Route: 0, Kind: InputOutput, OutputID: "o1", Content: "x"},
		{Route: 1, Kind: InputOutput, OutputID: "o1", Content: "x"}, // duplicate
		{Route: 1, Kind: InputOutput, OutputID: "o1", Content: "y"}, // different projection
		{Route: 1, Kind: InputSource, SourceID: "o1", Content: "x"}, // a source with the same ID
		{Route: 2, Kind: InputOutput, OutputID: "o2", Content: "x"}, // another output, same content
		{Route: 3, Kind: InputSource, SourceID: "o1", Content: "x"}, // duplicate source
	}
	got := dedupe(items)
	want := []int{0, 2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("dedupe kept %d items, want %d: %+v", len(got), len(want), got)
	}
	for i, idx := range want {
		if !reflect.DeepEqual(got[i], items[idx]) {
			t.Errorf("item %d = %+v, want %+v", i, got[i], items[idx])
		}
	}
}

// An anonymous route labels each output by its author's position in the
// producing layer and leaves out the reader's own; a named reader of the
// same layer sees the author with the label; a layer nobody reads
// anonymously carries no labels.
func TestRouterAnonymous(t *testing.T) {
	f := routerNewFixture(t)
	f.routerStandardOutputs()
	peers := f.routerConsumer("peers", []string{"alice", "bob", "chair"}, Route{From: "layer:review", Anonymous: true})
	named := f.routerConsumer("named", []string{"chair"}, Route{From: "layer:review"}, Route{From: "layer:notes"})

	got, err := f.routerResolve(peers)
	if err != nil {
		t.Fatal(err)
	}
	type labelled struct{ id, label string }
	view := func(items []InputItem) []labelled {
		out := make([]labelled, 0, len(items))
		for _, it := range items {
			if !it.Anonymous {
				t.Errorf("item %s of an anonymous route is not marked anonymous", it.OutputID)
			}
			out = append(out, labelled{it.OutputID, it.Label})
		}
		return out
	}
	// review participants: alice (A), bob (B), editor (C), whatever order
	// the turns were committed in and whoever reads them.
	wantAlice := []labelled{{"review-r001-bob", "Response B"}, {"review-r001-editor", "Response C"}, {"review-r002-bob", "Response B"}}
	if v := view(got.Participants["alice"]); !reflect.DeepEqual(v, wantAlice) {
		t.Errorf("alice sees %v, want %v (own outputs left out)", v, wantAlice)
	}
	wantBob := []labelled{{"review-r001-alice", "Response A"}, {"review-r001-editor", "Response C"}, {"review-r002-alice", "Response A"}}
	if v := view(got.Participants["bob"]); !reflect.DeepEqual(v, wantBob) {
		t.Errorf("bob sees %v, want %v", v, wantBob)
	}
	if n := len(got.Participants["chair"]); n != 5 {
		t.Errorf("chair (no outputs of its own) gets %d items, want 5", n)
	}

	byName, err := f.routerResolve(named)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range byName.Participants["chair"] {
		switch {
		case it.Anonymous:
			t.Errorf("named reader got anonymous item %s", it.OutputID)
		case it.LayerID == "review" && it.Label != responseLabel(slices.Index([]string{"alice", "bob", "editor"}, it.Author)):
			t.Errorf("chair: %s label %q, want the author's letter", it.OutputID, it.Label)
		case it.LayerID == "notes" && it.Label != "":
			t.Errorf("chair: %s of a layer nobody reads anonymously has label %q", it.OutputID, it.Label)
		}
	}

	again, err := f.routerResolve(peers)
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Errorf("second Resolve differs: %v", err)
	}
}

// A recipient left with nothing but its own outputs fails a required
// anonymous route and gets nothing from an optional one.
func TestRouterAnonymousOnlyOwn(t *testing.T) {
	f := routerNewFixture(t)
	f.routerAddOutput("notes", 1, "alice")
	required := f.routerConsumer("solo", []string{"alice"}, Route{From: "layer:notes", Anonymous: true})
	if _, err := f.routerResolve(required); err == nil || !strings.Contains(err.Error(), "its own outputs") {
		t.Fatalf("Resolve err = %v, want the own-outputs failure", err)
	}
	optional := f.routerConsumer("solo2", []string{"alice"}, Route{From: "layer:notes", Anonymous: true, Optional: true})
	got, err := f.routerResolve(optional)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(got.Participants["alice"]); n != 0 {
		t.Errorf("alice gets %d items, want none", n)
	}
}

func TestResponseLabel(t *testing.T) {
	for pos, want := range map[int]string{0: "Response A", 1: "Response B", 25: "Response Z", 26: "Response AA", 27: "Response AB", 51: "Response AZ", 52: "Response BA"} {
		if got := responseLabel(pos); got != want {
			t.Errorf("responseLabel(%d) = %q, want %q", pos, got, want)
		}
	}
}

// A random anonymous route never deals a record to its author, whatever the
// seed, and still deals every record once.
func TestRouterAnonymousRandomSkipsAuthor(t *testing.T) {
	for seed := range int64(50) {
		f := routerNewFixture(t)
		f.snap.Seed = seed
		f.routerAddOutput("notes", 1, "alice")
		f.routerAddOutput("notes", 1, "bob")
		layer := f.routerConsumer("dealt", []string{"alice", "bob"}, Route{From: "layer:notes", Anonymous: true, Distribute: DistributeRandom})
		got, err := f.routerResolve(layer)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		for pid, items := range got.Participants {
			if len(items) != 1 || items[0].Author == pid {
				t.Fatalf("seed %d: %s got %v, want the other author's record", seed, pid, routerIDs(items))
			}
		}
	}
}
