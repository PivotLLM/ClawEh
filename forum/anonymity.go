// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import "slices"

// The anonymity rules (DESIGN.md §8.11): an anonymous read of a layer
// must not let any reader learn the authors another way, statically
// (staticValidator.anonymous) or through the launcher's own access to the
// forum's files (preflight.launcherClones).

// anonymous checks an anonymous read of producer by layer l (the
// recipients are the route's): l must be after_round with one round and no
// moderator, so its readers never see one another by name; no recipient
// may see producer by name elsewhere (another route from it, its moderator,
// or taking part in it while it is per_turn or runs more than one round);
// and every recipient must have someone else's output to read.
func (v *staticValidator) anonymous(path string, l Layer, r Route, recipients []string, producer Layer) {
	if l.Delivery != DeliveryAfterRound || l.MaxRounds != 1 || l.Moderator != nil {
		v.addf(path, "layer %s reads %s anonymously, so it must be after_round with one round and no moderator", l.ID, producer.ID)
	}
	authors := producer.Participants
	if len(r.Authors) > 0 {
		authors = slices.DeleteFunc(slices.Clone(authors), func(a string) bool { return !slices.Contains(r.Authors, a) })
	}
	for _, pid := range recipients {
		who := v.participantName(pid)
		if !slices.ContainsFunc(authors, func(a string) bool { return a != pid }) {
			v.addf(path, "%s reads layer %s anonymously in layer %s but would only see its own responses", who, producer.ID, l.ID)
		}
		if slices.Contains(producer.Participants, pid) && (producer.Delivery == DeliveryPerTurn || producer.MaxRounds > 1) {
			v.addf(path, "%s takes part in layer %s, which is per_turn or has more than one round, so it can't read that layer anonymously in layer %s", who, producer.ID, l.ID)
		}
		if producer.Moderator != nil && producer.Moderator.Participant == pid {
			v.addf(path, "%s moderates layer %s, so it can't read that layer anonymously in layer %s", who, producer.ID, l.ID)
		}
		if named, ok := v.readsByName(producer.ID, pid); ok {
			v.addf(path, "%s reads layer %s anonymously in layer %s and by name in layer %s", who, producer.ID, l.ID, named)
		}
	}
}

// readsByName returns a layer in which participant pid receives producer's
// outputs through a route that is not anonymous, and whether there is one.
func (v *staticValidator) readsByName(producer, pid string) (string, bool) {
	reads := func(r Route, recipients []string) bool {
		kind, id, err := r.Producer()
		return err == nil && kind == RouteFromLayer && id == producer && !r.Anonymous && slices.Contains(recipients, pid)
	}
	for _, l := range v.cfg.Layers {
		for _, r := range l.Inputs {
			recipients := r.To
			if len(recipients) == 0 {
				recipients = l.Participants
			}
			if reads(r, recipients) {
				return l.ID, true
			}
		}
		if l.Moderator != nil {
			for _, r := range l.Moderator.Inputs {
				if reads(r, []string{l.Moderator.Participant}) {
					return l.ID, true
				}
			}
		}
	}
	return "", false
}

// launcherClones refuses the launching agent, or a clone of it, in a forum
// with an anonymous input: either acts as the launcher, whose file tools can
// read the forum's directory and so every author's name.
func (p *preflight) launcherClones() {
	if !usesAnonymous(p.cfg) {
		return
	}
	for _, id := range p.usedParticipants() {
		switch part := p.cfg.Participants[id]; {
		case part.Agent == p.env.Launcher:
			p.addf("participants."+id+".agent", "%s can read the forum's files, so it can't take part in an anonymous review", p.env.Launcher)
		case part.Clone == p.env.Launcher:
			p.addf("participants."+id+".clone", "a clone of %s can read the forum's files, so it can't take part in an anonymous review", p.env.Launcher)
		}
	}
}

// usesAnonymous reports whether any input of an enabled layer, or of its
// moderator, is anonymous.
func usesAnonymous(cfg *Config) bool {
	for _, l := range cfg.EnabledLayers() {
		for _, r := range l.Inputs {
			if r.Anonymous {
				return true
			}
		}
		if l.Moderator != nil {
			for _, r := range l.Moderator.Inputs {
				if r.Anonymous {
					return true
				}
			}
		}
	}
	return false
}
