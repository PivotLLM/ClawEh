# Remediation plan: dangling model references in agent model lists

Written 2026-09-26 from an investigation of a production symptom. This file is
self-contained: everything needed to implement the fix in a fresh context is
here. Do not ask the user for details that are answered below; the decisions in
section 4 are final unless the user overrides them.

## 1. The defect

### 1.1 Symptom

On the production instance (`/opt/claw`, agent `tanya`) the `/model` command
over Telegram listed:

```
Configured Models:
  0: DeepSeek 4 Pro
  1: Google Gemini 3.5 Flash  (Google API)
▶ 2: OR Grok High  (Openrouter Chat)
  ...
```

Entry 0 has no provider suffix. "DeepSeek 4 Pro" no longer exists in the
global `models` table. It is tanya's primary model (slot 0 of her `models`
list). When tanya was messaged on 2026-09-26 the first fallback attempt was
sent to the agent's shared default provider with the literal alias as the
model id, and OpenRouter answered:

```
WRN Fallback: candidate failed error={"Model":"DeepSeek 4 Pro","Provider":"",
"Reason":"format","Status":400, ... "DeepSeek 4 Pro is not a valid model ID"}
agent_id=tanya model="DeepSeek 4 Pro" provider= reason=format
```

Every turn for that agent pays a failed request before falling through to the
next candidate, and the cooldown tracker records a failure against an
empty-provider key.

### 1.2 Root cause

The WebUI model delete handler removes the entry from the global table but does
not remove references to it from agent model lists. Rename has a reference
repointer; delete has no counterpart.

- `web/backend/api/models.go` `handleDeleteModel` (around line 241): deletes
  `cfg.Models[idx]`, then the only cleanup is
  `if cfg.Agents.Defaults.DefaultModelName() == deletedModelName {
  cfg.Agents.Defaults.SetDefaultModel("") }`. Per-agent lists, defaults
  fallbacks, image/vision models, summarization chains and subagent model lists
  are untouched.
- `web/backend/api/models.go` `handleUpdateModel` (around line 169) calls
  `cfg.RenameModelReferences(oldName, mc.ModelName)` when the alias changes.
  That is the model to copy.

Production timeline from `/opt/claw/logs` supporting this: the alias resolved
to model id `deepseek-v4-pro` until 2026-09-14 10:00; on 2026-09-14 between
19:13 and 19:38 the operator did a model cleanup session (restart, wendy moved
off the alias, two other DeepSeek aliases renamed to "... Latest"); the alias
never resolves again afterwards and survives only as tanya's slot 0. Renamed
aliases were repointed correctly (agent `sam` followed the rename), which
isolates the delete path.

### 1.3 Why nothing caught it afterwards

Every layer assumes references are valid and none checks:

- `web/backend/api/config.go` `validateConfig` (around line 207): on
  `PUT /api/config` it runs `cfg.ValidateModels()` and
  `cfg.ValidateBindings()` only.
- `internal/gateway/helpers.go` around line 1074: the config-file reload
  watcher runs the same two checks and otherwise accepts the file.
- `internal/gateway/helpers.go` around line 152: startup calls
  `cfg.PruneInvalid()` (`config/config.go` around line 2889), which drops
  models with a bad provider and providers with a bad protocol, and never
  looks at who references a model.
- `providers/fallback.go` `ResolveCandidatesWithLookup` (lines 102 to 152):
  when the lookup misses, the alias is fed to `ParseModelRef(original, "")`
  (spawnllm `model_ref.go`), which for a bare string with no slash returns
  `{Provider: "", Model: original}`. The candidate is appended with an empty
  provider and empty alias. The warning
  `"fallback alias dropped (not enabled in models)"` at line 139 is only
  reached when the parser returns nil, which for a non-empty string requires a
  trailing slash, so in practice it never fires.
- `agent/instance.go` lines 262 to 283: the lookup function passed to the
  resolver matches enabled models by `model_name` via `cfg.GetModelConfig`,
  then enabled models by raw `model` id. Disabled models miss the lookup too.
- `commands/cmd_model.go` `renderModelList`: prints the provider only when
  non-empty, so a phantom entry renders as a bare name. That is the visible
  symptom, not the bug.
- `web/frontend/src/components/agents/model-selects.tsx`: the primary model
  `Select` lists only `configured && enabled` models, so a dangling value
  renders as the "Select model" placeholder. Nothing looks wrong unless the
  operator opens that agent.

### 1.4 Related defects found on the way

- `SetDefaultModel("")` (`config/config.go` around line 1317) writes an empty
  string into `Agents.Defaults.Models[0]` instead of removing the slot.
  `TestHandleDeleteModel_ClearsDefaultWhenDefaultDeleted`
  (`web/backend/api/models_extra_test.go` line 326) pins this by asserting
  `DefaultModelName() == ""`.
- `RenameModelReferences` (`config/config.go` line 2824) misses one reference
  site: `AgentConfig.Subagents.Models` (`SubagentsConfig`, `config/config.go`
  around line 834, JSON `subagents.models`).
- Disabling a model (`enabled: false` through `handleUpdateModel`, toggled by
  `web/frontend/src/components/models/models-page.tsx` line 69) has the same
  effect as delete at resolution time, because the lookup only considers
  enabled models.

## 2. Repository facts a fresh context needs

- Repo: `~/source/claw`, module `github.com/PivotLLM/ClawEh`, remote
  `origin git@github.com:PivotLLM/ClawEh.git`. Default branch `main`.
- Read `CLAUDE.md` at the repo root first. Key rules: project is **Released**
  (ask before any breaking change or compatibility shim); `CHANGELOG.md` entry
  goes under the topmost `## [x.y.z]` heading, which must equal `version` in
  `app/app.go` (it was `0.6.0` on 2026-09-26; if the topmost heading has
  already shipped, stop and tell the user to bump the version); `make test` is
  the full gate; `go test ./...` is the fast loop; example agent names in docs
  are Alice and Bob.
- Another worker is active in the main checkout (it was on
  `feature/channel-health-watchdog` on 2026-09-26). Work in a git worktree on a
  new branch `fix/model-dangling-references` cut from `origin/main`. Do not
  switch the main checkout's branch without asking.
- Config structs (`config/config.go`):
  - `Config.Models []ModelConfig` (`json:"models"`), `ModelConfig.ModelName`
    (`model_name`, the alias every reference uses), `ModelConfig.Enabled`.
  - `Config.Agents.Defaults` is `AgentDefaults` (line 1058): `Models []string`,
    `ImageModel string`, `ImageModelFallbacks []string`, `VisionModel string`,
    `VisionModelFallbacks []string`.
  - `Config.Agents.List []AgentConfig` (line 165). `AgentConfig` (line 280):
    `ID string` (`json:"id"`), `Models []string`, `SummarizationModels
    []string`, `Subagents *SubagentsConfig` with `Models []string`.
  - `Config.Summarization.Models []string` (line 174).
  - Before implementing, re-run
    `grep -n 'json:"[a-z_]*model[s]*[,"]' config/config.go` to confirm no
    reference field was added since this was written. Exclude `model_name`,
    the `ModelConfig.Model` raw id, STT/TTS `Model`, and the legacy
    `compress_model` migration around line 2315.
- Logging: `logger.WarnCF(component string, msg string, fields map[string]any)`
  and `logger.ErrorCF` in `logger/logger.go`. Config code uses component
  `"config"`, gateway `"gateway"`, resolver `"providers"`.
- Resolver: `providers/fallback.go`, `ResolveCandidatesWithLookup(cfg
  ModelConfig, defaultProvider string, lookup LookupFunc) []FallbackCandidate`.
  `ResolveCandidates` is the no-lookup wrapper. `FallbackCandidate{Provider,
  Model, Alias}`. Existing tests: `providers/fallback_test.go` from line 530
  (`TestResolveCandidates_*`, `TestResolveCandidatesWithLookup_*`).
- WebUI backend tests use `setupTestEnv(t)` in
  `web/backend/api/testenv_test.go`, which writes a default config with one
  provider `openai` and a model `custom-default` referenced by the defaults.
- The `/model` and `/list models` commands build entries in
  `agent/loop_commands.go` around line 179 from `agent.Candidates`.

## 3. Changes

Implement in this order. Each step ends with a passing `go test ./...`.

### 3.1 One walker over every model reference site

File: `config/config.go`, next to `RenameModelReferences`.

Add a private helper that visits every reference site once, so rename, remove
and validate cannot drift apart:

```go
// modelRef is one place a model alias is referenced from.
type modelRef struct {
    where  string    // human label, e.g. `agents.list[tanya].models`
    slice  *[]string // set for list sites
    scalar *string   // set for scalar sites
}

func (c *Config) modelRefSites() []modelRef
```

Sites, with labels for messages:

- `agents.defaults.models`, `agents.defaults.image_model`,
  `agents.defaults.image_model_fallbacks`, `agents.defaults.vision_model`,
  `agents.defaults.vision_model_fallbacks`
- `summarization.models`
- per agent (label with the agent `ID`): `agents.list[<id>].models`,
  `agents.list[<id>].summarization_models`,
  `agents.list[<id>].subagents.models` (only when `Subagents != nil`)

Rewrite `RenameModelReferences` on top of the walker. Behaviour is unchanged
except that `subagents.models` is now covered. Keep `renameInSlice` and
`renameScalar` if convenient.

### 3.2 Reference queries

Same file. Add:

```go
// ModelReferences returns the labelled sites that reference alias.
func (c *Config) ModelReferences(alias string) []string

// ValidateModelReferences reports every reference to an alias that is not
// an enabled model. Missing aliases are returned as errors; references to a
// model that exists but is disabled are returned separately as warnings.
func (c *Config) ValidateModelReferences() (errs []error, warnings []string)

// PruneDanglingModelReferences removes every reference to an alias that does
// not exist in Models (disabled models are left alone) and returns the
// labelled sites it removed. Scalars are blanked; list entries are deleted,
// not blanked.
func (c *Config) PruneDanglingModelReferences() []string
```

Empty strings in lists are skipped, not reported (the resolver already skips
them). Duplicate aliases in `Models` are legal (load balancing); "exists"
means at least one entry with that `model_name`; "enabled" means at least one
enabled entry. Error text must name the site label and the alias, for example
`agents.list[tanya].models: model "DeepSeek 4 Pro" does not exist`.

### 3.3 Fix `SetDefaultModel("")`

`config/config.go` around line 1317. When `modelName` is empty, remove slot 0
(`d.Models = d.Models[1:]`) instead of writing an empty string. Non-empty
behaviour unchanged.

### 3.4 Delete refuses while referenced

`web/backend/api/models.go` `handleDeleteModel`. After the index check:

```go
if refs := cfg.ModelReferences(deletedModelName); len(refs) > 0 {
    http.Error(w, fmt.Sprintf("model %q is still referenced by: %s. Repoint those first.",
        deletedModelName, strings.Join(refs, ", ")), http.StatusConflict)
    return
}
```

Remove the `DefaultModelName()` cleanup block; it is unreachable once
referenced models cannot be deleted. Update the existing test
`TestHandleDeleteModel_ClearsDefaultWhenDefaultDeleted` to assert 409 and that
the model is still present, and add `TestHandleDeleteModel_Unreferenced` that
adds a second, unreferenced model to the test config, deletes it, and gets 200.

Frontend: `web/frontend/src/components/models/delete-model-dialog.tsx` calls
`deleteModel(model.index)` at line 39. Confirm the API error text reaches the
user (check how `request` in `web/frontend/src/api/models.ts` surfaces
non-2xx bodies and what the dialog does in its catch). If the message is not
shown, show it inline in the dialog. No other frontend change is required.

### 3.5 Disable is allowed but visible

Do not block `enabled: false`. Instead the validation in 3.6 reports a
reference to a disabled model as a warning, and the resolver in 3.7 logs when
it drops one. No handler change.

### 3.6 Validate references at save, reload and startup

- `web/backend/api/config.go` `validateConfig`: after `ValidateModels`, call
  `ValidateModelReferences`; append each error's text to `errs`. Warnings are
  not returned to the client here (there is no channel for them); log them
  with `logger.WarnCF("config", ...)`.
- `internal/gateway/helpers.go` reload watcher (around line 1074): after
  `newCfg.ValidateModels()`, call `ValidateModelReferences`; if `errs` is
  non-empty, log with `logger.Errorf` and `logger.Warn("  Using previous valid
  config")` and call `alertConfigFileInvalid(a, configPath, err)` with a joined
  error, exactly like the two existing checks. Log warnings with `WarnCF`.
- `internal/gateway/helpers.go` startup (around line 152): after
  `PruneInvalid`, call `PruneDanglingModelReferences()`; for each removed site
  log `logger.WarnCF("gateway", "removed reference to unknown model",
  map[string]any{"site": ..., "model": ...})`. The on-disk config is left
  untouched, consistent with the comment above `PruneInvalid`. Then run
  `ValidateModelReferences` and log the disabled warnings.

Note the order: startup prunes invalid models first, then dangling references,
so a model dropped for having a bad provider also gets its references dropped.

### 3.7 Resolver drops unresolved aliases

`providers/fallback.go` `addCandidate` inside `ResolveCandidatesWithLookup`:
when `lookup != nil` and it returns `ok == false`, log
`logger.WarnCF("providers", "fallback alias dropped (not enabled in models)",
map[string]any{"alias": original})` and return without adding a candidate.
Keep the `ParseModelRef` path only for `lookup == nil` (the `ResolveCandidates`
wrapper and any other no-lookup caller). Grep for every caller of both
functions before changing (`grep -rn "ResolveCandidates" --include=*.go .`)
and confirm each lookup-bearing caller wants this behaviour; image and vision
chains may use their own resolvers.

`agent/instance.go` already logs an error when the chain ends up empty; leave
it.

Add `TestResolveCandidatesWithLookup_UnresolvedAliasIsDropped` in
`providers/fallback_test.go`: models `["ghost", "glm-5"]`, lookup resolves only
`glm-5`; expect exactly one candidate with `Provider == "openai"`, and no
candidate with an empty provider. Keep `TestResolveCandidates_*` (no lookup)
passing unchanged.

### 3.8 Tests for the config helpers

In `config/` (new file `model_references_test.go`):

- rename covers `subagents.models`;
- `ModelReferences` labels each site correctly for defaults, summarization,
  and a per-agent config with all three lists;
- `ValidateModelReferences` returns an error for a missing alias and a
  warning for a disabled one, and nothing for a valid config;
- `PruneDanglingModelReferences` removes list entries (not blanks), blanks
  scalars, leaves disabled references, and returns the labels;
- `SetDefaultModel("")` removes slot 0 and leaves the rest in order.

In `web/backend/api/` (`config_test.go` or a new file): `PUT /api/config` with
an agent whose `models` contains an unknown alias returns 400 and the error
names the agent id and the alias.

### 3.9 Changelog

`CHANGELOG.md`, under the topmost version heading (must match `app/app.go`):

- `Fixed`: deleting a model in the WebUI left agents referencing it; such an
  agent then sent the alias as a model id on every turn and got a 400 before
  falling back. Mention the `/model` listing showing an entry with no
  provider as the visible sign.
- `Changed`: `DELETE /api/models/{index}` now answers 409 while any agent,
  default, summarization or subagent chain references the model. Config save
  and file reload reject a config that references a model that does not exist;
  startup drops such references with a warning and continues. References to a
  disabled model are reported as warnings.

No changelog line for the test or refactor parts.

### 3.10 Docs

`docs/troubleshooting.md` already has a section about "not found in models"
(line 20). Add a short paragraph there: if `/model` shows an entry without a
provider, or the log says "fallback alias dropped", an agent references a model
that was deleted or disabled; fix it in the agent's model list.

## 4. Decisions already made

- Delete is refused, not silently cleaned. The operator must repoint agents
  first. Reason: a silent cleanup of slot 0 would change which model an agent
  runs on without anyone noticing, which is worse than a blocked delete.
- Disable is not refused. It is a legitimate temporary action; the cost is a
  warning at save, reload and startup and a resolver log line.
- Startup prunes in memory only and never rewrites `config.json`, matching
  `PruneInvalid`.
- Save and reload reject rather than prune, matching how they already treat an
  invalid model entry.
- `renderModelList` in `commands/cmd_model.go` is left as is; after 3.7 there
  are no provider-less entries to render.

## 5. Verification

1. `go build ./...` and `go test ./...` green.
2. `make test` green (full gate, lint included).
3. If `delete-model-dialog.tsx` changed: `cd web/frontend && pnpm run
   build:backend`, then the WebUI regression suite per `CLAUDE.md`
   (`make build`, restart `claw-dev`, wait for `/ready`, `make check-webui`).
   Never touch `claw-ai.service` or `update-claw.sh`.
4. Manual check on the dev instance (`~/.claw/config.json`, service
   `claw-dev`): give an agent a `models` list whose first entry is a name that
   does not exist, start the service, confirm the startup warning names the
   site and `/model` (in the WebUI chat or a bound channel) lists only real
   models with providers. Then edit the file the same way while running and
   confirm the reload is rejected with the "Using previous valid config"
   message and an operator alert. Then try deleting a referenced model in the
   WebUI models page and confirm the 409 text is shown.
5. Final grep for stale references: `SetDefaultModel`, `DefaultModelName`,
   "ClearsDefaultWhenDefaultDeleted".

## 6. Production repair (user action, not code)

Tanya's list on `/opt/claw` still holds the dead alias in slot 0. The config
is mode 0600 owned by `ai`. To confirm and repair, the operator can run:

```
sudo jq '.agents.list[] | select(.id=="tanya") | .models' /opt/claw/config.json
```

and then pick a real primary for tanya in the WebUI agents page. After the fix
ships, startup will also warn about any other dangling site.

## 7. Git

- Branch `fix/model-dangling-references` from `origin/main`, in a worktree.
- Commit at logical points (config helpers and tests; handler and tests;
  resolver and tests; changelog and docs). Push the branch. Open a draft PR.
- Do not merge. Do not tag.
