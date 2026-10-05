// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"cmp"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"slices"
)

// Seam (c): the router (rev 3 §4, which rev 5 inherits). It turns a
// layer's routes into the LayerInputs each participant and the moderator
// receive from sources and earlier layers. It knows nothing about turns
// within the layer: peer events inside a layer are the controller's
// visibility logic (turn.go).

// Router resolves routes against the forum's sources and committed outputs.
type Router struct {
	cfg  *Config
	snap *Snapshot
	// read returns a root-relative file (Store.ReadFile).
	read func(rel string) ([]byte, error)
}

// NewRouter builds a router over one forum's configuration and snapshot.
func NewRouter(cfg *Config, snap *Snapshot, read func(rel string) ([]byte, error)) *Router {
	return &Router{cfg: cfg, snap: snap, read: read}
}

// Resolve builds LayerInputs for layer from the outputs of earlier layers
// (produced: layer ID -> committed OutputRecords, from State). It is called
// once per layer, before its first dispatch, and the result is persisted;
// a resume reads inputs.json instead of calling Resolve again.
//
// For each route, in configuration order ("filter/project before
// distribution; concatenate routes in configuration order"):
//
//   - a source route yields one record: the materialised source's content
//     (Snapshot.Sources), narrowed by paths for a json source;
//   - a layer route yields the producing layer's outputs ordered by round
//     and then by the producing layer's participant order (orderOutputs),
//     reduced by select (last_per_participant keeps the newest record of
//     each author), filtered by authors, read as the published projection
//     or the full output per view, then narrowed by paths (Project); a
//     projection error fails the route even when it is optional;
//   - the recipients are `to`, else every participant of the layer; the
//     records are dealt (distribute): all gives every recipient the whole
//     bundle; same_participant gives each recipient the records it
//     authored; random shuffles the records with layerRand and assigns
//     each once, round-robin, across the recipients in configured order
//     (counts differ by at most one; some recipients may get nothing);
//   - a recipient whose bundle is empty fails the route unless it is
//     optional (so a random route with fewer records than recipients must
//     be optional); an optional route that selects nothing at all (an
//     empty selection or a disabled producer) has its index added to
//     Missing.
//
// Moderator routes are resolved the same way with the moderator as the
// only recipient. They take no `to`, and may use view full without it:
// the moderator is their one explicit recipient (ValidateStatic applies
// the same rule). An optional moderator route that selects nothing is not
// listed in Missing,
// whose indexes refer to the layer's inputs. Each recipient's list is then
// deduplicated (dedupe), keeping route order and then record order.
func (r *Router) Resolve(layer Layer, produced map[string][]OutputRecord) (*LayerInputs, error) {
	rng := layerRand(r.snap.Seed, layer.ID)
	out := &LayerInputs{LayerID: layer.ID, Participants: map[string][]InputItem{}}
	for i, route := range layer.Inputs {
		recipients := route.To
		if len(recipients) == 0 {
			recipients = layer.Participants
		}
		if err := r.checkRoute(layer, route, recipients, false); err != nil {
			return nil, routeError(layer.ID, "input", i, route, err)
		}
		items, err := r.routeItems(layer, route, i, produced)
		if err != nil {
			return nil, routeError(layer.ID, "input", i, route, err)
		}
		if len(items) == 0 {
			out.Missing = append(out.Missing, i)
			continue
		}
		dealt, err := distribute(route, items, recipients, rng)
		if err != nil {
			return nil, routeError(layer.ID, "input", i, route, err)
		}
		for _, pid := range recipients {
			out.Participants[pid] = append(out.Participants[pid], dealt[pid]...)
		}
	}
	for pid, items := range out.Participants {
		out.Participants[pid] = dedupe(items)
	}
	if layer.Moderator != nil {
		recipients := []string{layer.Moderator.Participant}
		for i, route := range layer.Moderator.Inputs {
			if len(route.To) > 0 {
				return nil, routeError(layer.ID, "moderator input", i, route, errors.New("a moderator route takes no to"))
			}
			if err := r.checkRoute(layer, route, recipients, true); err != nil {
				return nil, routeError(layer.ID, "moderator input", i, route, err)
			}
			items, err := r.routeItems(layer, route, i, produced)
			if err != nil {
				return nil, routeError(layer.ID, "moderator input", i, route, err)
			}
			if len(items) == 0 {
				continue
			}
			dealt, err := distribute(route, items, recipients, rng)
			if err != nil {
				return nil, routeError(layer.ID, "moderator input", i, route, err)
			}
			out.Moderator = append(out.Moderator, dealt[layer.Moderator.Participant]...)
		}
		out.Moderator = dedupe(out.Moderator)
	}
	return out, nil
}

// routeError attributes a routing failure to its layer and route.
func routeError(layerID, what string, index int, route Route, err error) error {
	return fmt.Errorf("layer %s %s %d (from %q): %w", layerID, what, index, route.From, err)
}

// checkRoute enforces the access rules that must hold at run time whatever
// static validation did: view full only with explicit recipients (a
// layer input's `to`; a moderator input's one recipient is the moderator,
// so moderator marks a moderator input, which may use view full), and
// recipients that belong to the consuming layer (or are its moderator).
func (r *Router) checkRoute(layer Layer, route Route, recipients []string, moderator bool) error {
	if route.View == ViewFull && len(route.To) == 0 && !moderator {
		return errors.New("view full requires explicit to recipients")
	}
	for _, pid := range route.To {
		if !slices.Contains(layer.Participants, pid) {
			return fmt.Errorf("recipient %q is not a participant of layer %s", pid, layer.ID)
		}
	}
	if len(recipients) == 0 {
		return errors.New("route has no recipients")
	}
	return nil
}

// routeItems resolves one route into its ordered, projected records before
// distribution. index is the route's position (InputItem.Route). An
// optional route with nothing to give returns an empty list and no error;
// a non-optional one with nothing to give is an error. A projection error
// is an error even for an optional route.
func (r *Router) routeItems(consumer Layer, route Route, index int, produced map[string][]OutputRecord) ([]InputItem, error) {
	kind, id, err := route.Producer()
	if err != nil {
		return nil, err
	}
	var items []InputItem
	switch kind {
	case RouteFromSource:
		items, err = r.sourceItem(route, index, id)
	case RouteFromLayer:
		producer, ok := r.cfg.Layer(id)
		if !ok {
			return nil, fmt.Errorf("unknown layer %q", id)
		}
		if !r.precedes(id, consumer.ID) {
			return nil, fmt.Errorf("layer %q does not precede layer %q (routes point backward only)", id, consumer.ID)
		}
		if !slices.Contains(r.snap.Layers, id) {
			if route.Optional {
				return nil, nil
			}
			return nil, fmt.Errorf("layer %q is disabled and the route is not optional", id)
		}
		items, err = r.outputItems(route, index, producer, orderOutputs(producer, produced[id]))
	default:
		return nil, fmt.Errorf("unknown producer kind %q", kind)
	}
	if err != nil {
		return nil, err
	}
	if len(items) == 0 && !route.Optional {
		return nil, errors.New("the route selects nothing and is not optional")
	}
	return items, nil
}

// precedes reports whether layer a comes before layer b in the
// configuration.
func (r *Router) precedes(a, b string) bool {
	ia := slices.IndexFunc(r.cfg.Layers, func(l Layer) bool { return l.ID == a })
	ib := slices.IndexFunc(r.cfg.Layers, func(l Layer) bool { return l.ID == b })
	return ia >= 0 && ib >= 0 && ia < ib
}

// sourceItem builds the single record of a source route from the
// materialised source file, narrowed by paths for a json source.
func (r *Router) sourceItem(route Route, index int, sourceID string) ([]InputItem, error) {
	if route.Distribute == DistributeSameParticipant {
		return nil, errors.New("distribute same_participant is valid for layer routes only")
	}
	rec, ok := r.snap.Sources[sourceID]
	if !ok {
		return nil, fmt.Errorf("unknown source %q", sourceID)
	}
	content, err := r.read(rec.File)
	if err != nil {
		return nil, fmt.Errorf("read source %q: %w", sourceID, err)
	}
	content, err = narrow(rec.Decode, content, route.Paths)
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", sourceID, err)
	}
	return []InputItem{{
		Route:    index,
		Kind:     InputSource,
		SourceID: sourceID,
		Format:   rec.Decode,
		Content:  string(content),
	}}, nil
}

// narrow applies a route's paths allowlist to content of format f. paths
// is rejected for anything but JSON.
func narrow(f Format, content []byte, paths []string) ([]byte, error) {
	if len(paths) == 0 {
		return content, nil
	}
	if f != FormatJSON {
		return nil, fmt.Errorf("paths apply to json content only, not %s", f)
	}
	return Project(content, paths)
}

// outputItems builds the records of a layer route from the producing
// layer's ordered outputs: select, authors, view and paths applied in that
// order.
func (r *Router) outputItems(route Route, index int, producer Layer, outs []OutputRecord) ([]InputItem, error) {
	switch route.Select {
	case "", SelectAll:
	case SelectLastPerParticipant:
		outs = lastPerParticipant(outs)
	default:
		return nil, fmt.Errorf("unknown select %q", route.Select)
	}
	if len(route.Authors) > 0 {
		outs = slices.DeleteFunc(slices.Clone(outs), func(o OutputRecord) bool {
			return !slices.Contains(route.Authors, o.ParticipantID)
		})
	}
	items := make([]InputItem, 0, len(outs))
	for _, o := range outs {
		var file string
		switch route.View {
		case "", ViewPublished:
			file = o.PublishedFile
		case ViewFull:
			file = o.ContentFile
		default:
			return nil, fmt.Errorf("unknown view %q", route.View)
		}
		content, err := r.read(file)
		if err != nil {
			return nil, fmt.Errorf("read output %s: %w", o.OutputID, err)
		}
		content, err = narrow(o.Format, content, route.Paths)
		if err != nil {
			return nil, fmt.Errorf("output %s of layer %s: %w", o.OutputID, producer.ID, err)
		}
		items = append(items, InputItem{
			Route:      index,
			Kind:       InputOutput,
			OutputID:   o.OutputID,
			LayerID:    o.LayerID,
			Round:      o.Round,
			Author:     o.ParticipantID,
			AuthorName: r.participantName(o.ParticipantID),
			Format:     o.Format,
			Content:    string(content),
		})
	}
	return items, nil
}

// participantName is the participant's transcript name: its configured
// name, else its ID.
func (r *Router) participantName(id string) string {
	if p, ok := r.cfg.Participants[id]; ok && p.Name != "" {
		return p.Name
	}
	return id
}

// lastPerParticipant keeps the newest record of each author over the whole
// producing layer (every round), preserving the order of outs, which is
// already round-major.
func lastPerParticipant(outs []OutputRecord) []OutputRecord {
	last := map[string]int{}
	for i, o := range outs {
		last[o.ParticipantID] = i
	}
	kept := make([]OutputRecord, 0, len(last))
	for i, o := range outs {
		if last[o.ParticipantID] == i {
			kept = append(kept, o)
		}
	}
	return kept
}

// orderOutputs sorts outputs by round, then by the position of their
// author in the producing layer's participants, regardless of the order
// in which the turns completed. An author not in the layer sorts after the
// listed ones; ties keep their input order. outs is not modified.
func orderOutputs(layer Layer, outs []OutputRecord) []OutputRecord {
	pos := func(pid string) int {
		if i := slices.Index(layer.Participants, pid); i >= 0 {
			return i
		}
		return len(layer.Participants)
	}
	sorted := slices.Clone(outs)
	slices.SortStableFunc(sorted, func(a, b OutputRecord) int {
		if c := cmp.Compare(a.Round, b.Round); c != 0 {
			return c
		}
		return cmp.Compare(pos(a.ParticipantID), pos(b.ParticipantID))
	})
	return sorted
}

// distribute deals items to recipients per the route's distribute mode
// and returns each recipient's bundle. It fails when a recipient's bundle
// is empty and the route is not optional. rng is used only for
// DistributeRandom; it is positioned by the caller so routes consume it
// in configuration order.
func distribute(route Route, items []InputItem, recipients []string, rng *rand.Rand) (map[string][]InputItem, error) {
	dealt := make(map[string][]InputItem, len(recipients))
	switch route.Distribute {
	case "", DistributeAll:
		for _, pid := range recipients {
			dealt[pid] = slices.Clone(items)
		}
	case DistributeSameParticipant:
		// Source routes are refused earlier (sourceItem): every item here
		// is an attributed output.
		for _, pid := range recipients {
			for _, it := range items {
				if it.Author == pid {
					dealt[pid] = append(dealt[pid], it)
				}
			}
		}
	case DistributeRandom:
		shuffled := slices.Clone(items)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		for i, it := range shuffled {
			pid := recipients[i%len(recipients)]
			dealt[pid] = append(dealt[pid], it)
		}
	default:
		return nil, fmt.Errorf("unknown distribute %q", route.Distribute)
	}
	if !route.Optional {
		for _, pid := range recipients {
			if len(dealt[pid]) == 0 {
				return nil, fmt.Errorf("recipient %q receives nothing and the route is not optional", pid)
			}
		}
	}
	return dealt, nil
}

// dedupe removes records identical to an earlier one for the same
// recipient (same source or output ID and the same projected content),
// keeping the first.
func dedupe(items []InputItem) []InputItem {
	type key struct {
		kind    InputKind
		id      string
		content string
	}
	seen := make(map[key]bool, len(items))
	kept := make([]InputItem, 0, len(items))
	for _, it := range items {
		k := key{kind: it.Kind, id: it.SourceID + "\x00" + it.OutputID, content: it.Content}
		if seen[k] {
			continue
		}
		seen[k] = true
		kept = append(kept, it)
	}
	return kept
}

// layerRand returns the deterministic generator for random distribution in
// one layer: PCG seeded from the forum seed and a hash of the layer ID, so
// every layer deals independently and a repeated Resolve (which never
// happens after inputs.json exists, but must be stable) gives the same
// result.
func layerRand(seed int64, layerID string) *rand.Rand {
	h := fnv.New64a()
	_, _ = h.Write([]byte(layerID))
	return rand.New(rand.NewPCG(uint64(seed), h.Sum64())) //nolint:gosec // G404/G115: routing fairness, not security; the seed is a bit pattern
}
