# forum/ — package design

The map of the package as implemented: how the files divide the work,
what crosses between the parts, the invariants nobody may break, what the
host must provide, the decisions agreed with the maintainer, and the
readings the code commits to where nothing else decides. The operator
guide is `docs/forum.md`; the agent guide is `readme/README.md`.

## 1. Dependencies

The package imports the standard library, `github.com/google/uuid`,
`github.com/PivotLLM/toolspec` (the tool contract, stdlib-only) and
`github.com/santhosh-tekuri/jsonschema/v6` (Draft 2020-12 JSON Schema; it
was already in `go.sum` as an indirect dependency and is now direct at the
same version). It imports nothing else from ClawEh: no agent loop, no
config, no logger, no bus. Everything the host provides arrives through
the interfaces in `host.go`.

**JSON Schema.** `schema_jsonschema.go` compiles schemas with
santhosh-tekuri v6 (`compileSchema`): Draft 2020-12 by default, every
external `$ref` refused (internal references only), violations flattened
into `SchemaViolationError.Messages` for the repair message. The host
supplies no validator.

## 2. Parts

| Part | Files |
| --- | --- |
| shared contract | `doc.go`, `host.go`, `errors.go`, `records.go` |
| (a) configuration | `config.go`, `config_schema.go`, `decode.go`, `validate.go`, `schema_jsonschema.go`, `jsonpointer.go`, `mergepatch.go` |
| (b) store and replay | `store.go`, `lock_unix.go`, `replay.go` |
| (c) router | `router.go` (projections in `jsonpointer.go`) |
| (d) controller | `controller.go`, `turn.go`, `moderator.go`, `recover.go`, `transcript.go` |
| (e) service and tools | `service.go`, `forums.go`, `tools.go`, `results_view.go`, `readme.go` (and `readme/`) |

- `records.go` is the wire format between the parts (every on-disk type,
  plus `State`, `Result`, `Summary`): (b) writes it, (d) fills it and (e)
  reports it.
- Everything but the service, the tools and the types they hand out is
  unexported; nothing outside the package uses the store, controller,
  router, decoding or validation directly.
- Tests live beside their part with a prefix (`st`, `rp`, `ctl`, `svc`,
  …) and use a `t.TempDir()` base directory. Parts (c), (d) and (e) are
  tested against fakes of the `host.go` interfaces; none needs ClawEh
  running. The service tests also run the real controller.

## 3. Data flow

```
tool calls ─► (e) Service.NewForum       createStore, WriteForumConfig  (b)
               SetConfig / UpdateConfig (merge patch): WriteForumConfig
               (refused while the latest run is running)
tool call ──► (e) Service.Launch(id)
                 │  Lock; undoUnstarted; latest run not running   (b)
                 │  ReadForumConfig                               (b)
                 │  decodeConfig → validateStatic → runPreflight  (a)
                 │  CreateRun(n+1); WriteConfig, WriteSource…     (b)
                 │  Agents.CreateClone / CreateFresh             host
                 │    (each ID added to .cleanup/<uuid>.<n>.agents.json)
                 │  WriteParticipants, set .cleanup/<uuid>.<n>.notice,
                 │  WriteSnapshot, AppendCommit(1, launched)
                 │  openForum (any failure: revertRun: delete agents,
                 │    RemoveRun)                                  (d)
                 │  supersede a paused run n (cancel, no notice)
                 ▼
            (d) openForum                 ← the SAME path a restart takes
                 │  verify, replayState, ReadCommits, ListAttempts (b)
                 │  checkCreated (Agents.Exists), regenerateTranscript
            (e) start → drive → forumController.Run
                 │  per layer: router.Resolve → WriteLayerInputs (c)(b)
                 │  per turn : composeTurnMessage → perform:
                 │               awaitModel: hold while Host.Cooldown > 0
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
                   (if .notice pending) → clear .notice; a failure, or a
                   result.json that cannot be written, is retried by the
                   keep-alive loop up to maxNoticeTries (5) times
```

What crosses between the parts:

- (a) → (e): `*Config`, `*Resolved` (models per participant, compiled
  schemas, effective moderator schemas, file source contents read once). (a) → (d): `Config`
  accessors (`Layer`, `EnabledLayers`, `Route.Producer`,
  `effectiveModeratorSchema`). (a) ← (c): `validPointer`,
  `checkProjection` for `share`/`paths` syntax.
- (b) → (d),(e): `forumStore` methods; `verify`, `replayState`,
  `loadState`, `replay`. Nothing reads `state.json` directly. (d) opens
  with `replayState`, which always rebuilds `State` from the commit log
  and rewrites `state.json` (lock holder only); (e)'s read-only callers
  (status, results) use `loadState`, which may use `state.json` as a
  cache and never writes it.
- (c) → (d): `router.Resolve(layer, produced) → *LayerInputs`, called once
  per layer by `startLayer`; `projectOutput` for published projections.
- (d) → (e): `openForum`, `forumController.Run/RequestPause/RequestCancel/
  State/Snapshot/Config/Participants`, and `buildResult`, the one builder
  of a `Result` (result.json and every partial manifest). (e) owns goroutines,
  locks, cleanup and notice; (d) owns everything between `openForum` and `end`.
- (e) → host: `Agents` (create/delete/touch/exists/models/may-target),
  `Notifier.ForumFinished`. (d) → host: `Messenger.Ask`, `Agents.Exists`
  (at `openForum`), `Logger`.

## 4. On-disk layout

```
<base>/                              <launching-agent-workspace>/forums (BaseDirName)
  .locks/<uuid>.run                  flock held by the running controller (LocksDir)
  .cleanup/<uuid>.<n>.agents.json    run n's temp agents still to delete (written at launch)
  .cleanup/<uuid>.<n>.notice         run n's completion notice not yet delivered
  .cleanup/<uuid>/                   a forum directory staged for removal (Remove)
  <uuid>/
    forum-meta.json                  the owner (ForumMeta), written once before forum.json
    forum.json                       the current configuration (indented, no HTML escaping)
    runs/<n>/                        run n = 1, 2, 3, …; never changed by a later run
      forum.json  snapshot.json  participants.json  state.json  result.json
      transcript.md
      sources/<id><ext>
      layers/<layer>/inputs.json
      layers/<layer>/calls/<turn>/<n>/request.json | reply.json
                                     | output<ext> | published<ext>
      commits/00000001.json …
```

Locks and cleanup staging live beside the forum directories, not inside
them, so removing one never removes the lock protecting it, and a `Remove`
is one atomic rename out of `listForums` followed by a plain delete. A
`forumStore` is either the forum's handle (run 0: the configuration, the runs,
the lock, removal) or one run (`Run`, `CreateRun`, `OpenRun`); every handle
derived from one opening shares the forum's lock. `Lock` sweeps the
temporary entries directly in the forum directory and in `runs/` (and the
whole run when the handle is one); `openForum` sweeps the run it opens
(`SweepRun`); earlier runs are never walked. A run directory is removed
(`RemoveRun`) by one rename to a temporary name and a delete, so a crash
leaves only a temporary entry (`TempPrefix`) the next `Lock` sweeps.

## 5. Invariants

1. **Commits are authoritative; `state.json` is a cache.** `replay` rebuilds
   `State` from `commits/` alone, and `replayApply` is the single fold both
   the controller (`commitWhen`, which folds into a copy first so a commit
   replay would refuse never reaches the log) and `replay` use, so they
   cannot disagree. `checkCommit` is the single structural check, run by
   `AppendCommit` before writing and by `replay` before folding.
   `AppendCommit(seq, c)` takes the seq the caller expects and refuses a
   mismatch before writing. Every attempt is a commit (`CommitAttempt`)
   written before its Ask, so `State.Calls` and each layer's budget come
   from the log, replied or not. What a participant was already sent is
   derived from the attempt commits too (`forumController.contact`); `State`
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
   unchanged as the same attempt if it has no reply (`AttemptRequest.Resent`:
   request.json is rewritten and the attempt reserved again, the one case
   where a `CommitAttempt` repeats an attempt number, only for the turn's
   newest, and at most once per attempt: `commitIndex` records the repeat,
   and `checkCommit` and `WriteAttemptRequest` refuse a second one);
   followed by a repair if its reply was rejected. An attempt already
   resent once (`AttemptRecord.ResendUsed`) that a restart cuts again has
   its message sent unchanged as the next attempt, a new reservation. So one
   restart uses up no attempt (`max_attempts_per_turn`), and a turn that
   brings the process down on every send ends `attempts_exhausted` within
   2 × `max_attempts_per_turn` restarts; every send is a call like any
   other (`max_calls`, layer budgets), and no limit is reset. An output
   whose turn had an attempt resent after a restart is marked
   (`OutputRecord.Resent`); `forum_results`, `forum_status` and the
   completion notice count the same outputs.
4. **The controller always starts from disk.** `openForum` is the only
   constructor; `Launch` writes the run's files, then opens. There is no
   in-memory-only state between dispatches.
5. **Private material never reaches `transcript.md` or a published file:**
   participant `instructions`, directed messages, rejected attempts, the
   moderator's `assessment`, full outputs where `share` narrows them. The
   transcript receives published outputs and the public part of decisions
   (decision, reason, guidance), in publication order. It is derived:
   `openForum` regenerates it from the log when it differs, rewriting the same
   file in place (`forumStore.ReplaceTranscript`) so `tail -f` keeps working; a
   torn rewrite is repaired at the next `openForum`. The transcript starts with
   a heading naming the forum and the run; each output is fenced in its
   format, and its author carries the "Response X" label when a route
   reads its layer anonymously. A result or partial manifest never shows
   an `after_round` round that was not published in its result layers;
   only when those have no output at all does it list the other layers'
   outputs (`Result.OtherLayers`: every committed one once the run has
   ended, the published ones while it runs), so the launcher can reach
   the work of a run that failed early. `forum_results` names
   `transcript.md` only once it exists.
6. **Inputs are resolved once.** `inputs.json` (random assignments
   included) is written before a layer's first dispatch and read back on
   resume; `Resolve` is never called for a layer that has the file.
7. **Snapshot over configuration.** Seed, models, layer order, result
   layers, limits, deadline, source digests and effective moderator
   schemas come from the run's `snapshot.json`, never recomputed from its
   `forum.json`, and never from the forum's current `forum.json`, which may
   have changed since the run was launched.
8. **One controller per forum.** At most one run of a forum is live, and
   only its latest. `<base>/.locks/<uuid>.run` is held from
   `Launch`/`Resume`/`Recover` until the run pauses or ends, and by every
   configuration change; a second process gets `ErrLocked`. The exclusive-create writes in the store
   (`request.json`, `commits/<seq>.json`, outputs) fail loudly if the lock
   is ever bypassed.
9. **Limits are checked before every dispatch** (`checkLimits`: deadline,
   forum `max_calls`, layer `max_calls`), never only between rounds, and
   repairs and moderator checks count like any other call.
10. **Hashes are verified, never regenerated.** `verify` checks
    `forum.json`, every source and every committed output and published
    projection against the recorded SHA-256, reading through the root
    without following symbolic links; a mismatch fails recovery with
    `ErrCorrupt`.
11. **Terminal before notice.** `result.json` is written by `end` (or by
    the service if `end` did not get to it), then the service deletes
    temporary agents, then notifies on a goroutine of its own, holding no
    lock, so a blocking `Notifier` never holds the forum. The
    `.cleanup/<uuid>.<n>.notice` marker, written at launch and cleared once
    the notice is delivered, lets a restart deliver a notice a crash or a
    shutdown interrupted. A notice that fails, or a `result.json` that
    cannot be written, keeps the marker and is retried by the keep-alive
    loop (`noticeFailed`, `retryNotices`); after `maxNoticeTries` (5) tries
    in one process it is given up with a warning naming the forum and the
    run, and the marker cleared.
12. **Temporary agents are created per run at launch and deleted at the
    run's terminal state or on delete.** Each created agent is added to the
    run's `.cleanup/<uuid>.<n>.agents.json` marker as soon as it exists; the marker
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
15. **Nothing starts after `Close`.** Every goroutine the service starts
    (runs, notices, the keep-alive loop) goes through `goTracked`/
    `goLocked`, which add to the WaitGroup under the lock `Close` takes to
    mark the service closed, so `Close` never waits while one is added and
    a `Recover` running into a `Close` starts nothing.

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
  It gets the run's launching chat (`Chat`: the channel and chat ID of the
  `forum_launch` call), which the service keeps in memory only
  (`launchChats`, keyed by forum and run, set before the run starts,
  dropped after the notice, on supersede and on delete); it is never read
  from the forum directory, so the host may deliver to it. It is the zero
  `Chat` for a run whose launch this process did not see (a restart).
  ClawEh posts the agent's answer to it, falling back to the launcher's
  default binding when it is unknown or the answer finds it offline or not
  found (`fallback_channel`/`fallback_chat_id` on the system message,
  `systemReplyFallback`). The notice text adds the end reason when it says
  more than the status: "run 1 finished: incomplete (deadline).", and, when
  there are any, the number of outputs (every layer's, the count
  `forum_status` gives) that came from turns resent after a restart.
- An Ask that returns without the reply (the wait elapsed, ctx ended:
  the call timeout, the run deadline, a cancel) should stop the
  participant's turn. ClawEh's `ForumHost.Ask` uses `askStoppingTurn`: the
  asked turn's context is cancelled once the asker stops waiting, so the
  model call is aborted. `Agents.Delete` of a participant still in that
  turn deletes it when the turn ends (`agentreg.DeleteWhenIdle`) and
  returns `ErrDeletePending`: not a failure (logged at Info, the notice is
  not held back), but the agent stays in the cleanup marker and the run in
  the keep-alive retries, so a restart before the turn ends (the pending
  deletion is not saved) still deletes it.
- An attempt whose wait the run deadline cut (a `timeout` reply at or
  after the deadline) ends the run `incomplete (deadline)`, not
  `failed (attempts_exhausted)`, even on the turn's last allowed attempt.
- `Host.OnStuck` (optional) tells the launcher and may raise an operator
  alert; it must not block.
- `Host.Cooldown` (optional, `ForumHost.Cooldown` over the shared cooldown
  tracker) reports when every model of a participant is in cooldown, with
  the model available first and the time left. `perform` then holds the
  turn before reserving its attempt (`awaitModel`, logged at Info naming the
  forum, participant and model), polling every `cooldownPoll`, and sends it
  once a model is available, with the reply's wait cut to what is left of
  the call timeout counted from the start of the hold. A cooldown that ends
  within the call timeout costs no attempt and no call. One that outlasts
  it, or ends with less than `minHeldWait` (1 s) left, is recorded as a
  `timeout` attempt without being sent: it uses an attempt and counts
  toward `max_calls`, like any timeout. Once the cooldown ends, a held turn
  waits a random `releaseDelay` (`releaseDelayMin` 2 s to `releaseDelayMax`
  5 s, within the call timeout, held again if the model is back in cooldown)
  so the turns it held do not reach the model at once; a turn that was not
  held is never delayed. Pause, cancel and the run deadline
  end the hold and the delay as they end any turn.
- `Agents` over the registry.
- The names other packages use for a forum directory are exported:
  `BaseDirName` (`forums`), `LocksDir` (`.locks`) and `TempPrefix`
  (`.tmp-`).

Tool failures are one sentence naming the forum; error chains and paths
are logged, never shown to the agent. Every message and log line names a
forum by `Ref` of the name in its current `forum.json` and its ID
(`storeRef`, `Service.ref`): "<name> (<id>)", or the ID alone when it has
no name or its configuration cannot be read (not found, damaged); log
lines say "forum <ref>" or "forum <ref> run <n>" (`logForum`, `logRun`,
the controller's `logName`). The controller's refusals use the same name,
read when it is opened (the configuration cannot change while a
controller is open). Records keep the name as configured (`Summary.Name`
and `Result.Name` are empty when it is unset); `Snapshot.Label` (the name,
or the ID) titles the transcript and the line heading each participant
message.

## 7. Decisions (agreed with the maintainer)

1. **Layer end reasons.** A layer that ran every round ends `round_limit`;
   `completed` is a run status only. Layer reasons:
   `round_limit`, `call_limit`, `moderator_stop`.
2. **Moderator after the last round:** not consulted (only while below
   `max_rounds`).
3. **Moderator schema.** `moderator.schema` names the schema of the
   required `assessment` only. The engine builds the effective decision
   schema (`EffectiveModeratorSchema`): closed object, required `decision`
   / `reason` / `guidance` (string or null; GUIDE nonempty, CONTINUE/STOP
   null), `assessment` when a schema is named, and an optional engine-owned
   `directed` array of `{to, text}` when `moderator.allow_directed` is
   true (`to` is an enum of the layer's participants). It is stored in
   `Snapshot.ModeratorSchemas` and sent with every moderator request.
4. **`conversation_view`:** `published` (default) or `full`.
5. **Host ceilings:** the install's maximums (`Ceilings`, from the
   `forum.limits` config section through `WithCeilings`, read at every
   validation and launch) cap `max_calls`, `max_duration_seconds`,
   `call_timeout_seconds`, `max_parallel_calls` and every layer's
   `max_calls`. A limit above its ceiling is rejected with an issue naming
   the ceiling, never capped silently. Runs keep the limits they launched
   with.
6. **Projection shape:** `share`/`paths` select object members
   into a fresh object with the enclosing structure kept; pointers are
   nonempty, non-overlapping, no array traversal; a missing path fails.
   `share` omitted publishes everything, `share: []` publishes `{}`
   (`Output.Share` is `*[]string` for that reason).
7. **Dedupe:** identical projected records (same source/output ID and
   content) per recipient are delivered once, first route wins.
8. **Cancel** cancels the in-flight ask's context; pause lets it finish.
9. **Fenced JSON** replies are unwrapped before validation.
10. **`keepAliveInterval` = 1 h.**
11. **Tools:** a forum is a configuration and runs (§7.21); `validate` and
    `launch` take only the forum's `id`; `launch` has no `options`.
12. **`model`** applies to fresh and clone participants only; it is
    rejected on an `agent` participant, which always runs on its own model.
13. **No `Whisper`** in the forum's `Messenger`; directed messages are
    forum-scoped and travel inside the next forum turn.
14. **No chat-id field** in attempt records.
15. **Source paths:** relative `file` paths resolve against the launching
    agent's workspace and must be readable by the agent.
16. **`inline` for `decode: json`** is any raw JSON value
    (`json.RawMessage`); for text/markdown it is a JSON string.
17. **`instructions`** is optional.
18. **Restart:** `Recover` applies to each forum's latest run what it did
    to a forum: it resumes a queued (after appending the missing
    `CommitLaunched`) or running run, resumes a pausing/cancelling one so
    the controller finishes the transition, leaves a paused run paused
    (keep-alive only) and finishes the terminal work of a terminal one
    (result.json, agents, notice). It finishes staged removals, undoes
    every run whose launch died before its snapshot (`revertRun`: the
    forum is as it was before that launch), deletes the agents such an
    undone run left, finishes an earlier run's pending markers
    (`finishEarlier`: terminal work, or the supersede a crash cut short),
    leaves a forum with no run alone, and removes a directory missing
    `forum-meta.json` or `forum.json` (`listIncomplete`), which includes
    every directory of the earlier development layout.
19. **Launch is all or nothing** for its caller: any failure, including
    `openForum` failing after `CommitLaunched` (nothing has been dispatched),
    deletes the agents created so far and removes the new run
    (`revertRun`). The forum is locked before anything is written.
20. **Control operations** (service): repeating pause, resume or cancel
    is harmless; cancellation dominates (pause and resume of a cancelling
    forum are refused, and a pending cancel must win over a pending pause
    in the controller); resume clears a pending pause (`CommitResumed`
    from pausing or paused); pause, resume and cancel take over an
    interrupted forum. Pause, resume and cancel act on the latest run; a
    forum with no run refuses them. `delete` works on a forum whose latest
    run is not running (a new, paused or ended one, and a corrupt one),
    removes every run, deleting an ID that does not exist is refused as
    not found (`ErrNotFound`: "Forum <id> was not found."), and a forum whose
    agents cannot all be deleted is kept. Operations on one ID are
    serialised; live lookups are restricted to the caller's base directory.
21. **Runs.** `forum_new` creates `<base>/<uuid>/forum.json` (`{}`) and
    an empty `runs/`; the status is `new` until the first run. The
    configuration need not be valid until validate or launch. It can be
    changed (template, import, merge patch) whenever the latest run is not
    running (`busy`: not paused and not terminal; a run interrupted by a
    restart counts as running), under the control lock and the forum's
    lock, and is always written formatted (`formatConfig`: indented, no
    HTML escaping, formatting a formatted document changes nothing).
    `forum_config_update` is RFC 7386 (`mergePatch`: the stored
    configuration's member order and untouched values are kept byte for
    byte; members the patch adds are appended). The host hands `config`
    and `changes` over decoded, so the agent's own key order is not kept
    (a decoded object is encoded in alphabetical order) and numbers pass
    through float64 (an integer above 2^53 loses precision); a host that
    passes raw JSON (`json.RawMessage`) keeps both. Source `file` paths are
    resolved by the host exactly as the agent's file tools read them
    (`ToolHost.ResolveFile`: workspace, folders, mounts such as `maestro/`).
    Every launch validates the current configuration and starts run n+1
    in `runs/<n+1>/` from the beginning, with its own copy of the
    configuration (`forum.json`, whose digest is the snapshot's
    `ConfigDigest`), sources, participants and temporary agents; earlier
    runs are never changed. A run whose directory has no `snapshot.json`
    did not start and is undone at the next launch or recovery
    (`undoUnstarted`). A launch is refused while the latest run is
    running; a paused latest run is superseded once the new run is written
    and open (`supersede`: cancelled through its controller, the notice
    marker cleared after the cancel is committed so no notice is sent, its
    agents deleted), and a latest run whose records are damaged only has
    its agents deleted. `config_changed` (status) compares the digest of
    the formatted current configuration with the latest run's
    `ConfigDigest` for integrity only: whether the configuration changed is
    decided by comparing the current `forum.json` with the run's copy in
    canonical form (`canonicalConfig`: decoded with `UseNumber`, numbers
    normalised, re-encoded with sorted members), so an export imported
    back or a patch and its revert is no change. Resuming a paused run is
    refused once they differ ("Forum <name> (<id>): the configuration
    changed; launch to start a new run."), so a run always executes the configuration it was
    launched with. The next run number is one more than the highest started
    run or run named by a cleanup marker (`nextRun`), so no new run shares
    an earlier run's marker. A launch checks that the service is not
    closing before it supersedes a paused run, and the paused run leaves
    the keep-alive set only once its cancel is committed. An earlier run
    left non-terminal without markers (a supersede cut short) is
    superseded at recovery too. Results of any run check that run's
    snapshot names the scope's agent and the run. Status and
    results take an optional run number (default the latest); pause,
    resume and cancel act on the latest run; delete removes the forum and
    every run. `name` is optional; see §6 for how a forum is named. A
    forum whose latest run names another launcher is `ErrCorrupt` when
    opened in a scope (`Service.open`), and so is one whose
    `forum-meta.json` (`ForumMeta`: the owner, written once by `forum_new`
    before `forum.json`) names another agent (`checkForumOwner`); both wrap
    `errForeign`, which `delete` refuses too. A directory without
    `forum-meta.json` or `forum.json` is not a forum: it is not listed or
    opened, and recovery removes it. There is no template store:
    configurations move between forums by export and import.

## 8. Readings the implementation takes

Each is a choice the code commits to where nothing else decides. Listed so
the reader of the affected part knows it is a choice, not a requirement.

1. **Attempt reservation granularity.** The exact request and its
   attempt/budget reservation are committed before dispatch: one
   `CommitAttempt` per attempt (plus `request.json`), so
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
   accepted attempt, `output<ext>` / `published<ext>`; usage is not
   recorded (asks run full agent turns and return no usage).
   (b).
8. **Who writes `transcript.md`** and when: the controller, at
   publication (per turn for `per_turn`, per round for `after_round`;
   decision and guidance after each moderated commit). (d).
9. **`Result.Omissions`** (`buildResult`) names result layers that did
   not run or did not end, turns of a started round without a committed
   output, and `after_round` rounds whose committed outputs were never
   published. (d), (e).
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
    (`writeInputs`). The transcript and results keep real names; the
    transcript adds the label to the author ("Alice (Response A)") so the
    letters the reviews use can be read back.
    Validation (no reader may learn the authors another way): not on a
    source or with `same_participant`; the reading layer is `after_round`,
    one round, no moderator (so no moderator input is ever anonymous); no
    recipient reads the producer by name through another route, moderates
    it, or takes part in it while it is `per_turn` or multi-round; every
    recipient has another author (after `authors`); and preflight refuses
    the launcher or a clone of it in a forum with any anonymous input,
    since either can read the forum directory. (a), (c), (d).
