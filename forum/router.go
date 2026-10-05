// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"hash/fnv"
	"math/rand/v2"
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
//     be optional); a disabled producer with an optional route yields
//     nothing and the route index is added to Missing.
//
// Moderator routes are resolved the same way with the moderator as the
// only recipient. Each recipient's list is then deduplicated (dedupe),
// keeping route order and then record order.
func (r *Router) Resolve(layer Layer, produced map[string][]OutputRecord) (*LayerInputs, error) {
	rng := layerRand(r.snap.Seed, layer.ID)
	out := &LayerInputs{LayerID: layer.ID, Participants: map[string][]InputItem{}}
	for i, route := range layer.Inputs {
		items, err := r.routeItems(route, i, produced)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			out.Missing = append(out.Missing, i)
			continue
		}
		recipients := route.To
		if len(recipients) == 0 {
			recipients = layer.Participants
		}
		dealt, err := distribute(route, items, recipients, rng)
		if err != nil {
			return nil, err
		}
		for pid, got := range dealt {
			out.Participants[pid] = append(out.Participants[pid], got...)
		}
	}
	for pid, items := range out.Participants {
		out.Participants[pid] = dedupe(items)
	}
	if layer.Moderator != nil {
		for i, route := range layer.Moderator.Inputs {
			items, err := r.routeItems(route, i, produced)
			if err != nil {
				return nil, err
			}
			out.Moderator = append(out.Moderator, items...)
		}
		out.Moderator = dedupe(out.Moderator)
	}
	return out, nil
}

// routeItems resolves one route into its ordered, projected records before
// distribution. index is the route's position (InputItem.Route). An
// optional route with nothing to give returns an empty list and no error;
// a non-optional one with nothing to give is an error.
func (r *Router) routeItems(route Route, index int, produced map[string][]OutputRecord) ([]InputItem, error) {
	kind, id, err := route.Producer()
	if err != nil {
		return nil, err
	}
	switch kind {
	case RouteFromSource:
		return r.sourceItem(route, index, id)
	case RouteFromLayer:
		layer, _ := r.cfg.Layer(id)
		return r.outputItems(route, index, layer, orderOutputs(layer, produced[id]))
	}
	return nil, errNotImplemented
}

// sourceItem builds the single record of a source route from the
// materialised source file, narrowed by paths for a json source.
func (r *Router) sourceItem(route Route, index int, sourceID string) ([]InputItem, error) {
	return nil, errNotImplemented
}

// outputItems builds the records of a layer route from the producing
// layer's ordered outputs: select, authors, view and paths applied in that
// order. A disabled producer (not in Snapshot.Layers) yields nothing.
func (r *Router) outputItems(route Route, index int, producer Layer, outs []OutputRecord) ([]InputItem, error) {
	return nil, errNotImplemented
}

// orderOutputs sorts outputs by round, then by the position of their
// author in the producing layer's participants, regardless of the order
// in which the turns completed.
func orderOutputs(layer Layer, outs []OutputRecord) []OutputRecord {
	return outs
}

// distribute deals items to recipients per the route's distribute mode
// and returns each recipient's bundle. It fails when a recipient's bundle
// is empty and the route is not optional. rng is used only for
// DistributeRandom; it is positioned by the caller so routes consume it
// in configuration order.
func distribute(route Route, items []InputItem, recipients []string, rng *rand.Rand) (map[string][]InputItem, error) {
	return nil, errNotImplemented
}

// dedupe removes records identical to an earlier one for the same
// recipient (same source or output ID and the same projected content),
// keeping the first.
func dedupe(items []InputItem) []InputItem {
	return items
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
