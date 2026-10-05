# forum/ — package design

Implements `private/Forum_Greenfield_Specification.md` (revision 5), with
the §4, §6 and §8 details it inherits from revision 3
(`private/Forum_Greenfield_Specification.rev3.md`). This file is the map
for implementing the skeleton in parallel: who owns which files, what
crosses the seams, the invariants nobody may break, the order of work, and
the readings the skeleton commits to where the spec leaves room.

The skeleton compiles, vets and lints clean (`go build ./...`,
`go vet ./forum/...`, `golangci-lint run ./forum/...`). Leaf functions
return `errNotImplemented`; the exported entry points already carry the
control flow described in their doc comments, so an implementer fills in
leaves rather than redesigning the flow.

## 1. Dependencies

The package imports the standard library, `github.com/google/uuid`,
`github.com/PivotLLM/toolspec` (the tool contract, stdlib-only) and
`github.com/santhosh-tekuri/jsonschema/v6` (Draft 2020-12 JSON Schema; it
was already in `go.sum` as an indirect dependency and is now direct at the
same version). It imports nothing else from ClawEh: no agent loop, no
config, no logger, no bus. Everything the host provides arrives through
the interfaces in `host.go`.

**JSON Schema.** Validation is behind `SchemaValidator` / `CompiledSchema`.
`schema_jsonschema.go` is the adapter over santhosh-tekuri v6: Draft
2020-12 by default, every external `$ref` refused (§3 "internal references
only"), violations flattened into `SchemaViolationError.Messages` for the
repair message. The host passes `forum.JSONSchemaValidator{}` as
`Host.Schemas`; with a nil validator any configuration naming a schema
or having an enabled moderated layer (whose decision schema is always
validated) fails preflight with `ErrSchemasUnavailable` naming them.

## 2. Seams and owners

| Seam | Files | Owner | Spec |
| --- | --- | --- | --- |
| shared contract (frozen) | `doc.go`, `host.go`, `errors.go`, `records.go` | architect; changes by agreement of every owner | §2.1, §8 |
| (a) configuration | `config.go`, `decode.go`, `validate.go`, `schema_jsonschema.go` | config engineer | §3, §3.1, §3.2, rev 3 §4 route rules, §6 fields, §2.4 preflight |
| (b) store and replay | `store.go`, `replay.go` | store engineer | §8, rev 3 §8 |
| (c) router | `router.go`, `jsonpointer.go` | router engineer | rev 3 §4 |
| (d) controller | `controller.go`, `turn.go`, `moderator.go`, `recover.go` | controller engineer | §5, §6, §2.3, §8 restart |
| (e) service and tools | `service.go`, `tools.go` | service engineer | §9, §2.2 |

Rules that keep the seams independent:

- A seam edits only its own files. Something another seam needs is asked
  for, not added in place.
- `records.go` is the wire format between seams (every on-disk type, plus
  `State`, `Result`, `Summary`). Adding a field there is a cross-seam change:
  announce it, because (b) writes it, (d) fills it and (e) reports it.
- Tests live beside their seam (`store_test.go`, `router_test.go`, …) and
  use a `t.TempDir()` base directory. Seams (c), (d) and (e) can be tested
  against fakes of the `host.go` interfaces; none needs ClawEh running.

## 3. Data flow

```
tool call ──► (e) Service.Launch
                 │  Decode → ValidateStatic → Preflight          (a)
                 │  CreateStore → Lock, WriteConfig, WriteSource… (b)
                 │  Agents.CreateClone / CreateFresh             host
                 │    (each ID added to .cleanup/<uuid>.agents.json)
                 │  WriteParticipants, set .cleanup/<uuid>.notice,
                 │  WriteSnapshot, Commit(launched)
                 │  Open (any failure: delete agents, Remove)    (d)
                 ▼
            (d) Controller.Run            ← the SAME path a restart takes
                 │  Verify, ReplayState, ListAttempts            (b)
                 │  per layer: Router.Resolve → WriteLayerInputs (c)(b)
                 │  per turn : composeTurnMessage → ask:
                 │               WriteAttemptRequest → Commit(attempt)
                 │               → Messenger.Ask → WriteAttemptReply
                 │             validateOutput → WriteOutput → Commit(turn)
                 │  moderator: composeModeratorMessage → ask → parseDecision
                 │             → Commit(moderated)
                 │  end      : Commit(ended) → WriteResult
                 ▼
            (e) completeTerminal: result.json if missing → deleteTempAgents
                 → Notifier.ForumFinished (if .notice pending) → clear .notice
```

What crosses each boundary:

- (a) → (e): `*Config`, `*Resolved` (models per participant, compiled
  schemas, effective moderator schemas, file source contents read once). (a) → (d): `Config`
  accessors (`Layer`, `EnabledLayers`, `Route.Producer`,
  `EffectiveModeratorSchema`). (a) ← (c): `ValidPointer`,
  `CheckProjection` for `share`/`paths` syntax.
- (b) → (d),(e): `Store` methods; `Verify`, `ReplayState`, `LoadState`,
  `Replay`. Nothing reads `state.json` directly. (d) opens with
  `ReplayState`, which always rebuilds `State` from the commit log and
  rewrites `state.json` (lock holder only); (e)'s read-only callers
  (status, results) use `LoadState`, which may use `state.json` as a
  cache and never writes it.
- (c) → (d): `Router.Resolve(layer, produced) → *LayerInputs`, called once
  per layer by `startLayer`; `Project` for published projections.
- (d) → (e): `Open`, `Controller.Run/RequestPause/RequestCancel/State/
  Snapshot/Participants`. (e) owns goroutines, locks, cleanup and notice;
  (d) owns everything between `Open` and `end`.
- (e) → host: `Agents` (create/delete/touch/exists/models/may-target),
  `Notifier.ForumFinished`. (d) → host: `Messenger.Ask`, `Agents.Exists`
  (at `Open`), `Logger`.

## 4. On-disk layout (rev 3 §8, rev 5 §8)

```
<base>/                              <launching-agent-workspace>/forums
  .locks/<uuid>.run                  flock held by the running controller
  .cleanup/<uuid>.agents.json        temp agents still to delete (written at launch)
  .cleanup/<uuid>.notice             completion notice not yet delivered (written at launch)
  .cleanup/<uuid>/                   a root staged for removal (Remove)
  <uuid>/
    forum.json  snapshot.json  participants.json  state.json  result.json
    transcript.md
    sources/<id><ext>
    layers/<layer>/inputs.json
    layers/<layer>/calls/<turn>/<n>/request.json | reply.json
                                     | output<ext> | published<ext>
    commits/00000001.json …
```

Locks and cleanup staging live beside the roots, not inside them, so
removing a root never removes the lock protecting it, and a `Remove` is one
atomic rename out of `ListForums` followed by a plain delete.

## 5. Invariants

1. **Commits are authoritative; `state.json` is a cache.** `Replay` rebuilds
   `State` from `commits/` alone, and `applyCommit` is the single fold both
   the controller and `Replay` use, so they cannot disagree. Every attempt
   is a commit (`CommitAttempt`) written before its Ask, so `State.Calls`
   and each layer's budget come from the log, replied or not.
2. **One output per work ID.** A turn ID (`r<round>-<participant>`,
   `m<round>` for a moderator check) gets exactly one `CommitTurn` / `CommitModerated`,
   however many attempts were reserved. `Run` skips any turn with a
   committed output, which is how a resume lands on the first unfinished
   action.
3. **Request, reservation, Ask, reply — in that order.** `request.json`,
   then `CommitAttempt`, then `Messenger.Ask`, then `reply.json`. On resume
   the latest attempt of an unfinished turn is: adopted if it has an
   accepted reply (crash between reply and output commit); resent
   unchanged if it has no reply; followed by a repair if its reply was
   rejected. Restart resets no limit (§5).
4. **The controller always starts from disk.** `Open` is the only
   constructor; `Launch` writes everything, then opens. There is no
   in-memory-only state between dispatches.
5. **Private material never reaches `transcript.md` or a published file:**
   participant `instructions`, directed messages, rejected attempts, the
   moderator's `assessment`, full outputs where `share` narrows them. The
   transcript receives published outputs and the public part of decisions
   (decision, reason, guidance), in publication order.
6. **Inputs are resolved once.** `inputs.json` (random assignments
   included) is written before a layer's first dispatch and read back on
   resume; `Resolve` is never called for a layer that has the file.
7. **Snapshot over configuration.** Seed, models, layer order, result
   layers, limits, deadline, source digests and effective moderator
   schemas come from `snapshot.json`, never recomputed from `forum.json`.
8. **One controller per forum.** `<base>/.locks/<uuid>.run` is held from
   `Launch`/`Resume`/`Recover` until the run pauses or ends; a second
   process gets `ErrLocked`. The exclusive-create writes in the store
   (`request.json`, `commits/<seq>.json`, outputs) fail loudly if the lock
   is ever bypassed.
9. **Limits are checked before every dispatch** (`checkLimits`: deadline,
   forum `max_calls`, layer `max_calls`), never only between rounds, and
   repairs and moderator checks count like any other call.
10. **Hashes are verified, never regenerated.** `Verify` checks
    `forum.json`, every source and every committed output and published
    projection against the recorded SHA-256, reading through the root
    without following symbolic links; a mismatch fails recovery with
    `ErrCorrupt`.
11. **Terminal before notice.** `result.json` is written by `end` (or by
    the service if `end` did not get to it), then the service deletes
    temporary agents, then notifies (§9 Completion). The
    `.cleanup/<uuid>.notice` marker, written at launch and cleared after
    the notice attempt, lets a restart deliver a notice a crash
    interrupted; a failed notice is logged, not retried.
12. **Temporary agents are created at launch and deleted at a terminal
    state or on delete.** Each created agent is added to the
    `.cleanup/<uuid>.agents.json` marker as soon as it exists; the marker
    is the only record of what is still to delete. `deleteTempAgents(ctx,
    store)` deletes the agents it lists (`ErrNotFound` counts as done),
    keeps the failures in it and clears it when empty, so a restart
    finishes an interrupted deletion and never deletes twice. The
    registry TTL is only a backstop; paused forums' agents (from the same
    marker) are touched every `keepAliveInterval` (1 h) and once at
    recovery.

## 6. Order of implementation

Everything in one row can proceed in parallel; a row needs the rows above
it only for integration tests, not to start.

| Step | Work | Needs |
| --- | --- | --- |
| 1 | (a) `Decode` + `checkDuplicateKeys`, `ValidateStatic`, `EffectiveModeratorSchema`, adapter tests; (b) `Store` writes/reads, `Lock`, `AppendCommit`/`ReadCommits`, `Remove`/`ListStaged`; (c) `jsonpointer.go` | nothing |
| 2 | (a) `Preflight` against a fake `Agents`; (b) `Replay`, `Verify`, `ReplayState`, `LoadState`; (c) `Router.Resolve` and helpers against hand-built `OutputRecord`s | step 1 of the same seam |
| 3 | (d) `ask`, `validateOutput`, `parseDecision`, `repairMessage`, `composeTurnMessage`, `composeModeratorMessage`, `eligibleEvents`, `pendingDirected`, `applyCommit`, `runRoundAfterRound`, transcript writers, `compileSchemas`, `checkCreated`, `hostFailure` | (b) store for tests; (c) `Project` |
| 4 | (e) `allocate`, `createParticipants`, `resume`, `Cancel` (paused path), `recoverOne`, `deleteTempAgents`, `touchPaused`, `summaryOf`, `resultOf`, `launchOptions`, `toolOutcome`, the remaining handlers | (b), (d) |
| 5 | Integration: a fake `Messenger` driving a two-layer forum end to end; crash-and-resume at every commit boundary (kill after each `AppendCommit`, reopen, assert `Replay` equals the pre-crash state and the run completes); pause/cancel races; limits | all |
| 6 | Host wiring in ClawEh (outside this package): `tools/forum` provider building `ToolHost` and `Host` from `tools.ToolDeps`, the `forum` permission, `Agents` over `agentreg`, `Messenger` over core `Ask`, `Host.Schemas = forum.JSONSchemaValidator{}`, startup `Recover` over every agent's `<workspace>/forums` | 1–5 |

Within (d), `runTurn` and `runModerator` already contain the attempt loop
including adoption and resend; the leaves to write are listed in step 3.
Within (e), `Launch`, `Status`, `Results`, `Delete`, `Recover`, `Close`,
`start`, `finish` carry the flow; step 4 lists the leaves.

## 7. Decisions (agreed with the maintainer)

1. **Layer end reasons.** A layer that ran every round ends `round_limit`
   (§5 literal); `completed` is a run status only. Layer reasons:
   `round_limit`, `call_limit`, `moderator_stop`.
2. **Moderator after the last round:** not consulted (rev 3 §6 "while
   below max_rounds").
3. **Moderator schema.** `moderator.schema` names the schema of the
   required `assessment` only. The engine builds the effective decision
   schema (`EffectiveModeratorSchema`): closed object, required `decision`
   / `reason` / `guidance` (string or null; GUIDE nonempty, CONTINUE/STOP
   null), `assessment` when a schema is named, and an optional engine-owned
   `directed` array of `{to, text}` when `moderator.allow_directed` is
   true (`to` is an enum of the layer's participants). It is stored in
   `Snapshot.ModeratorSchemas` and sent with every moderator request.
4. **`conversation_view`:** `published` (default) or `full` (rev 3 §6).
5. **Host ceilings:** a limit above its ceiling is rejected with an issue
   naming the ceiling, never capped silently.
6. **Projection shape (rev 3 §4):** `share`/`paths` select object members
   into a fresh object with the enclosing structure kept; pointers are
   nonempty, non-overlapping, no array traversal; a missing path fails.
   `share` omitted publishes everything, `share: []` publishes `{}`
   (`Output.Share` is `*[]string` for that reason).
7. **Dedupe:** identical projected records (same source/output ID and
   content) per recipient are delivered once, first route wins.
8. **Cancel** cancels the in-flight ask's context; pause lets it finish.
9. **Fenced JSON** replies are unwrapped before validation.
10. **`keepAliveInterval` = 1 h.**
11. **Tools:** `validate` and `launch` take exactly one of `config`
    (object) and `config_file` (string); `launch` has no `options`.
12. **`model`** applies to fresh and clone participants only; it is
    rejected on an `agent` participant, which always runs on its own model.
13. **No `Whisper`** in the forum's `Messenger`; directed messages are
    forum-scoped and travel inside the next forum turn.
14. **No chat-id field** in attempt records (rev 5 has no channel).
15. **Source paths:** an inline configuration's relative `file` paths
    resolve against the launching agent's workspace; a `config_file`'s
    against that file's directory; both must be readable by the agent.
16. **`inline` for `decode: json`** is any raw JSON value
    (`json.RawMessage`); for text/markdown it is a JSON string.
17. **`instructions`** is optional.
18. **Restart:** `Recover` automatically resumes queued (after appending
    the missing `CommitLaunched`) and running forums, resumes
    pausing/cancelling ones so the controller finishes the transition,
    leaves paused forums paused (keep-alive only), finishes the terminal
    work of terminal ones (result.json, agents, notice) and staged
    removals, and discards a launch that died before its snapshot.
19. **Launch is all or nothing** for its caller: any failure, including
    `Open` failing after `CommitLaunched` (nothing has been dispatched),
    deletes the agents created so far and removes the directory. The
    store is locked right after `CreateStore`.
20. **Control operations** (service): repeating pause, resume or cancel
    is harmless; cancellation dominates (pause and resume of a cancelling
    forum are refused, and a pending cancel must win over a pending pause
    in the controller); resume clears a pending pause (`CommitResumed`
    from pausing or paused); pause, resume and cancel take over an
    interrupted forum. `delete` works on paused or terminal forums (and a
    corrupt one), deleting an absent ID succeeds, and a forum whose agents
    cannot all be deleted is kept. Operations on one ID are serialised;
    live lookups are restricted to the caller's base directory.

## 8. Still inferred (rev 3 and rev 5 do not say)

Each is a reading the skeleton takes; none contradicts the spec. Listed so
the owner of the affected seam knows it is a choice, not a requirement.

1. **Attempt reservation granularity.** Rev 3 §8 says "commit the exact
   request and attempt/budget reservation" before dispatch; the skeleton
   makes that one `CommitAttempt` per attempt (plus `request.json`), so
   the budget is derivable from the log alone. (b), (d).
2. **Visible-event cutoff.** `after_round` turns see commits up to the
   seq frozen at dispatch; `per_turn` turns see up to the current seq;
   events are filtered to the turn's own layer (cross-layer content only
   arrives through routes). (d).
3. **`last_per_participant` across rounds** keeps the newest record of
   each author over the whole producing layer, not per round. (c).
4. **`same_participant` recipients** must all be participants of the
   producing layer (checked statically); a recipient who authored nothing
   gets an empty bundle, which fails unless the route is optional. (a),
   (c).
5. **Random with too few records** can only be checked statically for
   source producers (one record); for layer producers it is a runtime
   failure of a non-optional route. (a), (c).
6. **Moderator inputs** take no `to` (the moderator is the sole recipient)
   and no `distribute` other than `all`. (a).
7. **Attempt directories** hold `request.json`, `reply.json` and, for an
   accepted attempt, `output<ext>` / `published<ext>`; rev 3's "usage"
   is not recorded (rev 5 asks run full agent turns and return no usage).
   (b).
8. **Who writes `transcript.md`** and when: the controller, at
   publication (per turn for `per_turn`, per round for `after_round`;
   decision and guidance after each moderated commit). (d).
9. **`Result.Omissions`** names result layers that did not end and turns
   without a committed output; rev 3 lists "omissions, errors, usage"
   without defining them. (e).
10. **A forum whose run goroutine dies** (process restart while
    `running`) keeps its on-disk status; "interrupted" is a condition
    (`running` with no live controller) rather than a status, and
    `Recover`/`Resume` detect it. (e).
