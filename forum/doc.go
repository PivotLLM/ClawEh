// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// Package forum runs a configured interaction among participants (agents,
// clones of agents, or fresh temporary agents) in ordered layers, retains
// every input, attempt, decision and result on disk, and exposes the
// lifecycle as a tool suite.
//
// The package imports no agent-loop code. Everything it needs from the host
// arrives through the small interfaces in host.go (Messenger, Agents,
// Notifier, Logger); ClawEh wires them in and mounts the tools from
// tools.go under the "forum" namespace.
//
// The files, by part (DESIGN.md §2):
//
//   - host.go, errors.go, records.go: what the host provides, the
//     sentinel errors, and the on-disk record types every part shares.
//   - config.go, config_schema.go, decode.go, validate.go,
//     schema_jsonschema.go, jsonpointer.go, mergepatch.go: the
//     configuration contract and its JSON Schema, strict decoding,
//     validation and preflight, JSON Schema compilation, `share`/`paths`
//     projections, and the merge patch of forum_config_update.
//   - store.go, lock_unix.go, replay.go: the store (one directory per
//     forum, one per run), its lock, and the verification and replay of
//     the commit log into State.
//   - router.go: resolving a layer's routes into the inputs each
//     participant receives.
//   - controller.go, turn.go, moderator.go, recover.go, transcript.go:
//     executing layers, composing messages, validating and repairing
//     output, moderation, pause/resume/cancel, opening a run (also after a
//     restart) and the transcript.
//   - service.go, forums.go, tools.go, results_view.go, readme.go: the
//     lifecycle service, forums and their runs, the tool definitions, the
//     forum_results view, and the guide and templates (readme/).
//
// Invariants every part relies on (DESIGN.md §5):
//
//   - The commit log (commits/) is authoritative; state.json is a cache that
//     replay rebuilds from the commits.
//   - Exactly one output is ever committed per turn (work) ID, however many
//     attempts were sent.
//   - Private material (participant instructions, directed messages, failed
//     attempts, moderator assessments) never reaches transcript.md or a
//     published output.
//   - The controller always starts from disk: Launch writes the run and
//     then opens it the same way restart recovery does.
package forum
