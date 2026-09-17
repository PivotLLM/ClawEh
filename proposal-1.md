# Maestro Integration Improvements (Proposal 1)

### 1. Model Selection & Delegation
Allow task workers to use specialized models by passing `req.LLMID` from Maestro's dispatch request into `SyncRunner.RunSync(ctx, req.Prompt, req.LLMID)` in `dispatcher.go`. If `req.LLMID` matches an alias or model name in `agent.Candidates`, the worker executes on that model; otherwise, it falls back to the agent's primary model.

### 2. Runner & Concurrency Controls
Add an optional Maestro configuration block to `AgentConfig` (e.g. `maestro_config: { max_concurrent: 2, rate_limit: ... }`) with conservative defaults. Pass these settings into `mconfig.Prepare()` so users running local models (like Ollama) or tiered API keys can avoid concurrency bottlenecks and rate-limit errors.

### 3. Mounts as Reference Directories
Automatically map an agent’s existing directory mounts (`agent.mounts`) into Maestro's reference library via `mconfig.WithReferenceDirs()`. This makes user-provided documentation, standards, and manuals directly accessible to Maestro's reference tools (`maestro_file_get`, `maestro_file_search`).

### 4. System Prompt Guidance
When `maestro: true`, inject a concise system prompt layer explaining the three-domain architecture (Reference, Playbooks, Projects) and how to initiate workflows. This gives the model the awareness needed to choose Maestro orchestration for complex, multi-step tasks rather than attempting them sequentially in chat.

### 5. Tool Discovery & Context Optimization
Expose a small set of primary entry-point tools (such as `project_create`, `playbook_list`, and `task_create`) as default visible tools, while keeping specialized worker tools behind progressive discovery. This prevents all 61 tools from cluttering the context window while ensuring the model always knows orchestration is available.

### 6. Observability & Log Integration
Route Maestro's per-agent log events into ClawEh's central logger tagged with the `[maestro]` component so they appear directly in the Web UI log viewer. Additionally, provide a basic project status view in the Web UI to let users track active projects and task outcomes.

### 7. Project Lifecycle & Pruning
Introduce an automatic retention policy and a CLI command (e.g. `claw maestro prune --days 30`) to archive or delete completed project runs and scratch files in `<workspace>/maestro/projects/`, preventing unbounded disk accumulation over time.
