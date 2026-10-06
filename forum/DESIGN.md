# forum/ — package design

Implements `private/Forum_Greenfield_Specification.md` (revision 5), with
the §4, §6 and §8 details it inherits from revision 3
(`private/Forum_Greenfield_Specification.rev3.md`). This file is the map
of the package as implemented: how the files divide the work, what
crosses the seams, the invariants nobody may break, what the host must
provide, and the readings the code commits to where the spec leaves room.

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

## 2. Seams

| Seam | Files | Spec |
| --- | --- | --- |
| shared contract | `doc.go`, `host.go`, `errors.go`, `records.go` | §2.1, §8 |
| (a) configuration | `config.go`, `decode.go`, `validate.go`, `schema_jsonschema.go` | §3, §3.1, §3.2, rev 3 §4 route rules, §6 fields, §2.4 preflight |
| (b) store and replay | `store.go`, `replay.go` | §8, rev 3 §8 |
| (c) router | `router.go`, `jsonpointer.go` | rev 3 §4 |
| (d) controller | `controller.go`, `turn.go`, `moderator.go`, `recover.go` | §5, §6, §2.3, §8 restart |
| (e) service and tools | `service.go`, `draft.go`, `mergepatch.go`, `tools.go` | §9, §2.2 |

- `records.go` is the wire format between seams (every on-disk type, plus
  `State`, `Result`, `Summary`): (b) writes it, (d) fills it and (e)
  reports it.
- Tests live beside their seam with a seam prefix (`st`, `rp`, `ctl`,
  `svc`, …) and use a `t.TempDir()` base directory. Seams (c), (d) and (e)
  are tested against fakes of the `host.go` interfaces; none needs ClawEh
  running. The service tests also run the real controller.

## 3. Data flow

```
tool calls ─► (e) Service.NewDraft      CreateStore, WriteDraft       (b)
               SetDraftConfig / UpdateDraft (merge patch): WriteDraft
tool call ──► (e) Service.Launch(id)
                 │  IsDraft → Lock; ReadDraft                    (b)
                 │  Decode → ValidateStatic → Preflight          (a)
                 │  WriteConfig, WriteSource…                    (b)
                 │  Agents.CreateClone / CreateFresh             host
                 │    (each ID added to .cleanup/<uuid>.agents.json)
                 │  WriteParticipants, set .cleanup/<uuid>.notice,
                 │  WriteSnapshot, AppendCommit(1, launched)
                 │  Open (any failure: revertLaunch: delete
                 │    agents, ResetToDraft)                      (d)
                 │  ClearDraft once the run has started
                 ▼
            (d) Open                      ← the SAME path a restart takes
                 │  Verify, ReplayState, ReadCommits, ListAttempts (b)
                 │  checkCreated (Agents.Exists), regenerateTranscript
            (e) start → drive → Controller.Run
                 │  per layer: Router.Resolve → WriteLayerInputs (c)(b)
                 │  per turn : composeTurnMessage → perform:
                 │               reserve: WriteAttemptRequest → Commit(attempt)
                 │               dispatch: Messenger.Ask → WriteAttemptReply
                 │             storeOutput: validateOutput → WriteOutput → Commit(turn)
                 │  moderator: composeModeratorMessage → perform → parseDecision
                 │             → Commit(moderated)
                 │  end      : Commit(ended) → WriteResult (buildResult)
                 ▼
            (e) completeTerminal: result.json if missing (buildResult)
                 → deleteTempAgents (failure: retried by the keep-alive loop)
                 → notify (own goroutine): Notifier.ForumFinished
                   (if .notice pending) → clear .notice
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
  Snapshot/Config/Participants`, and `buildResult`, the one builder of a
  `Result` (result.json and every partial manifest). (e) owns goroutines,
  locks, cleanup and notice; (d) owns everything between `Open` and `end`.
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
    draft.json                       Draft (owner, configuration) until launch
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
   `State` from `commits/` alone, and `replayApply` is the single fold both
   the controller (`commitWhen`, which folds into a copy first so a commit
   Replay would refuse never reaches the log) and `Replay` use, so they
   cannot disagree. `checkCommit` is the single structural check, run by
   `AppendCommit` before writing and by `Replay` before folding.
   `AppendCommit(seq, c)` takes the seq the caller expects and refuses a
   mismatch before writing. Every attempt is a commit (`CommitAttempt`)
   written before its Ask, so `State.Calls` and each layer's budget come
   from the log, replied or not. What a participant was already sent is
   derived from the attempt commits too (`Controller.contact`); `State`
   does not duplicate it.
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
   (decision, reason, guidance), in publication order. It is derived:
   `Open` regenerates it from the log when it differs, rewriting the same
   file in place (`Store.ReplaceTranscript`) so `tail -f` keeps working; a
   torn rewrite is repaired at the next `Open`. A result or partial
   manifest never shows an `after_round` round that was not published.
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
    temporary agents, then notifies (§9 Completion) on a goroutine of its
    own, holding no lock, so a blocking `Notifier` never holds the forum.
    The `.cleanup/<uuid>.notice` marker, written at launch and cleared
    after the notice attempt, lets a restart deliver a notice a crash or
    a shutdown interrupted; any other failed notice is logged, not
    retried.
12. **Temporary agents are created at launch and deleted at a terminal
    state or on delete.** Each created agent is added to the
    `.cleanup/<uuid>.agents.json` marker as soon as it exists; the marker
    is the only record of what is still to delete. `deleteTempAgents(ctx,
    store)` deletes the agents it lists (`ErrNotFound` counts as done),
    keeps the failures in it and clears it when empty, so a restart
    finishes an interrupted deletion and never deletes twice. A terminal
    forum whose deletion failed is retried every `keepAliveInterval`
    until it succeeds; the registry TTL is only a backstop. The agents of
    paused and running forums (from the same marker) are touched every
    `keepAliveInterval` (1 h), and a paused forum's once at recovery.
13. **No request is left unfinished.** `RequestPause`/`RequestCancel` are
    refused (`errRunEnded`, an `ErrInvalidState` naming the forum) once
    `Run` has returned; the service's `drive` re-reads the state after
    `Run` returns and calls `Run` again while it is pausing or
    cancelling, so a request accepted just before `Run` returned (or
    while it failed) is always settled. A service operation refused with
    `errRunEnded` waits for the run to be released and takes the forum
    over from disk.
14. **A run error never fails a forum silently.** `ErrCorrupt` met by
    `Run` ends the forum failed with `EndCorrupt` (logged at Error with
    the cause). A host shutdown (`ErrShuttingDown`, or the service's
    context ending) leaves the attempt uncertain and the forum as it is,
    logged at Info; it resumes at the next start. Any other run error is
    logged at Error naming the forum and reported once through
    `Host.OnStuck`; the forum continues with `forum_resume` or at the
    next start. A forum `Recover` cannot reopen is reported the same way.

## 6. Host wiring (ClawEh, outside this package)

The `tools/forum` provider builds the `ToolHost` from `tools.ToolDeps` and
mounts `forum.Tools(svc, toolHost)` under the `forum` namespace, gated by
the `forum` switch; `Host` is `agent.ForumHost`, and the gateway creates the
service before the agent loop builds any tools
(`internal/gateway/forum.go`). Startup calls `svc.Recover` over the
`<workspace>/forums` of every agent with the switch on, once the loop
accepts asks; shutdown calls `svc.Close` before the agent loop stops. Every
`Messenger.Ask` context carries the forum and its launcher
(`AskInfoFromContext`), so the host names the launcher as the sender. The
contract beyond the interface signatures:

- `Messenger.Ask` runs the turn at the maximum sub-agent depth and reports
  a host shutdown as an error wrapping `ErrShuttingDown`, never as
  `Reply{Outcome: cancelled}`.
- `ToolHost.Scope` refuses a call made at the maximum sub-agent depth
  (`ErrForumDepth`) or by a temporary agent a forum created
  (`ErrForumTurn`); the tools turn each into its own tool error.
- The forum directory lives in the launcher's workspace and is not trusted
  for privileged decisions: a store opened in a scope refuses a snapshot
  naming another launcher (`ErrCorrupt`), `Agents.Delete`/`Touch` name the
  launcher so the host acts only on forum participants it owns, and the
  host checks every ask against the launcher's current permissions.
- `Notifier.ForumFinished` hands the notice off and returns; it is called
  on its own goroutine, and `Close` waits for it until its context ends.
- `Host.OnStuck` (optional) tells the launcher and may raise an operator
  alert; it must not block.
- `Agents` over the registry, `Host.Schemas = forum.JSONSchemaValidator{}`.

Tool failures are one sentence naming the forum; error chains and paths
are logged, never shown to the agent.

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
11. **Tools:** a forum is configured as a draft (§7.21); `validate` and
    `launch` take only the draft's `id`; `launch` has no `options`.
12. **`model`** applies to fresh and clone participants only; it is
    rejected on an `agent` participant, which always runs on its own model.
13. **No `Whisper`** in the forum's `Messenger`; directed messages are
    forum-scoped and travel inside the next forum turn.
14. **No chat-id field** in attempt records (rev 5 has no channel).
15. **Source paths:** relative `file` paths resolve against the launching
    agent's workspace and must be readable by the agent.
16. **`inline` for `decode: json`** is any raw JSON value
    (`json.RawMessage`); for text/markdown it is a JSON string.
17. **`instructions`** is optional.
18. **Restart:** `Recover` automatically resumes queued (after appending
    the missing `CommitLaunched`) and running forums, resumes
    pausing/cancelling ones so the controller finishes the transition,
    leaves paused forums paused (keep-alive only), finishes the terminal
    work of terminal ones (result.json, agents, notice) and staged
    removals, leaves drafts alone, undoes a launch that died before its
    snapshot (`revertLaunch`: the draft is kept) and removes a `draft.json`
    left beside a snapshot.
19. **Launch is all or nothing** for its caller: any failure, including
    `Open` failing after `CommitLaunched` (nothing has been dispatched),
    deletes the agents created so far and resets the directory to the
    draft (`ResetToDraft`). The draft is locked before anything is written.
20. **Control operations** (service): repeating pause, resume or cancel
    is harmless; cancellation dominates (pause and resume of a cancelling
    forum are refused, and a pending cancel must win over a pending pause
    in the controller); resume clears a pending pause (`CommitResumed`
    from pausing or paused); pause, resume and cancel take over an
    interrupted forum. `delete` works on paused or terminal forums (and a
    corrupt one), deleting an absent ID succeeds, and a forum whose agents
    cannot all be deleted is kept. Operations on one ID are serialised;
    live lookups are restricted to the caller's base directory.
21. **Drafts.** `forum_config_new` creates `<base>/<uuid>/draft.json`
    (`Draft`: owner, times, the configuration as raw JSON, `{}` at first);
    a draft need not be valid until validate or launch. A forum is a draft
    while it has `draft.json` and no `snapshot.json`; only a draft can be
    changed (template, import, merge patch), under the control lock and the
    store lock. `forum_config_update` is RFC 7386 (`mergePatch`: member
    order kept, untouched values byte for byte). Launch keeps the ID: it
    writes `forum.json` (the draft's configuration, indented) and the rest
    into the same directory and removes `draft.json` once the run has
    started. A `draft.json` naming another owner is `ErrCorrupt`, like a
    snapshot naming another launcher. Status shows `draft`; pause, resume,
    cancel and results refuse a draft; delete removes one; export works on
    any forum. There is no template store: configurations move between
    forums by export and import.

## 8. Still inferred (rev 3 and rev 5 do not say)

Each is a reading the implementation takes; none contradicts the spec. Listed so
the reader of the affected seam knows it is a choice, not a requirement.

1. **Attempt reservation granularity.** Rev 3 §8 says "commit the exact
   request and attempt/budget reservation" before dispatch; the implementation
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
9. **`Result.Omissions`** (`buildResult`) names result layers that did
   not run or did not end, turns of a started round without a committed
   output, and `after_round` rounds whose committed outputs were never
   published; rev 3 lists "omissions, errors, usage" without defining
   them. (d), (e).
10. **A forum whose run goroutine dies** (process restart while
    `running`) keeps its on-disk status; "interrupted" is a condition
    (`running` with no live controller) rather than a status, and
    `Recover`/`Resume` detect it. (e).
11. **Anonymous inputs** (`Route.Anonymous`, layer routes only). The
    router labels each output of a layer that some route reads
    anonymously "Response <letter>" by its author's position in the
    producing layer's `participants` (`responseLabel`), so the letter is
    per producing layer and the same for every reader; an anonymous
    route's records are shown by label only, a named reader's as
    "<name> (Response A)". Each recipient's own records are left out
    (`withoutOwn`), and `random` deals a record to the next recipient in
    turn that did not write it. `InputItem.Label` and
    `InputItem.Anonymous` are persisted in `inputs.json`, so a resume
    shows the same labels. An optional anonymous route that gives a
    recipient nothing is written as "No other responses are available."
    (`writeInputs`). The transcript and results keep real names.
    Validation (no reader may learn the authors another way): not on a
    source or with `same_participant`; the reading layer is `after_round`,
    one round, no moderator (so no moderator input is ever anonymous); no
    recipient reads the producer by name through another route, moderates
    it, or takes part in it while it is `per_turn` or multi-round; every
    recipient has another author (after `authors`); and Preflight refuses
    the launcher or a clone of it in a forum with any anonymous input,
    since either can read the forum directory. (a), (c), (d).
