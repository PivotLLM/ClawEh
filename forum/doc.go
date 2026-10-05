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
// Notifier, Logger, SchemaValidator); ClawEh wires them in and mounts the
// tools from tools.go under the "forum" namespace.
//
// The package is split along five seams, each in its own files (see
// DESIGN.md):
//
//   - config.go, decode.go, validate.go: the configuration contract, strict
//     decoding and preflight validation.
//   - records.go, store.go, replay.go: the on-disk record types, the store
//     (one directory per forum) and the replay of commits into State.
//   - router.go, jsonpointer.go: resolving a layer's routes into the inputs
//     each participant receives.
//   - controller.go, turn.go, moderator.go, recover.go: executing layers,
//     composing messages, validating and repairing output, moderation,
//     pause/resume/cancel and restart recovery.
//   - service.go, tools.go: the lifecycle service and the tool definitions.
//
// Invariants every seam relies on:
//
//   - The commit log (commits/) is authoritative; state.json is a cache that
//     Replay rebuilds from the commits.
//   - Exactly one output is ever committed per turn (work) ID, however many
//     attempts were sent.
//   - Private material (participant instructions, directed messages, failed
//     attempts, moderator assessments) never reaches transcript.md or a
//     published output.
//   - The controller always starts from disk: Launch writes the forum and
//     then opens it the same way restart recovery does.
package forum
