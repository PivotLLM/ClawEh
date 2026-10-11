import { repairAgentId, sameAgentId } from "@/components/agents/agent-model"

// Providers worth surfacing first in the picker — the rest follow alphabetically.
export const COMMON_PROVIDERS = [
  "OpenAI",
  "Anthropic",
  "Google API",
  "OpenRouter Chat",
  "Groq",
  "DeepSeek",
  "Mistral",
  "Ollama",
]

export const CUSTOM_MODEL = "__custom__"
// Models surfaced as "(Recommended)" in the wizard (and sorted to the top),
// keyed by provider name → model id.
export const RECOMMENDED_MODEL: Record<string, string> = {
  "OpenRouter Chat DeepSeek": "~deepseek/deepseek-flash-latest",
}
// Sentinel for "let the CLI use its own default model" — maps to a model whose
// id is the CLI protocol (e.g. "antigravity-cli"), which the provider treats as
// "pass no --model arg".
export const CLI_DEFAULT = "__cli_default__"

export type TestState = "idle" | "testing" | "ok" | "warn" | "fail"

// uniqueAgentId turns an agent display name into an agent id by the agent id
// rule, keeping the name's case as the server's suggestion does
// (repairAgentId; "agent" when the name leaves nothing usable), with "-2",
// "-3", ... appended while it names an agent in taken (ignoring case,
// sameAgentId), the base shortened to keep the id within the 64-character
// limit.
export function uniqueAgentId(name: string, taken: Iterable<string>): string {
  const base = repairAgentId(name) || "agent"
  const ids = [...taken]
  const isTaken = (id: string) => ids.some((t) => sameAgentId(t, id))
  let id = base
  for (let n = 2; isTaken(id); n++) {
    const suffix = `-${n}`
    id = base.slice(0, 64 - suffix.length).replace(/[-_]+$/, "") + suffix
  }
  return id
}

export interface StepDef {
  key: string
  title: string
}

// wizardAgentList is agents.list after the wizard's agent step: the default
// agent renamed and put on the chosen model in place (first setup), or a new
// agent added (an install that already has a usable model, or no default
// agent). The wizard grants no tools; an agent has only the tools its list
// names, which the operator picks on the Agents page.
export function wizardAgentList(
  rawList: Record<string, unknown>[],
  agentName: string,
  modelName: string,
  alreadyConfigured: boolean,
): Record<string, unknown>[] {
  const defaultIdx = rawList.findIndex((a) => a.default === true)
  if (!alreadyConfigured && defaultIdx >= 0) {
    return rawList.map((a, i) =>
      i === defaultIdx
        ? { ...a, name: agentName.trim(), models: [modelName] }
        : a,
    )
  }
  const existingIds = new Set(rawList.map((a) => String(a.id ?? "")))
  const newAgent: Record<string, unknown> = {
    id: uniqueAgentId(agentName, existingIds),
    name: agentName.trim(),
    models: [modelName],
  }
  if (defaultIdx < 0) newAgent.default = true
  return [...rawList, newAgent]
}
