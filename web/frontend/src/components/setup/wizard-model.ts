import { normalizeAgentId } from "@/components/agents/agent-model"

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
// rule (normalizeAgentId; "agent" when the name leaves nothing usable), with
// "-2", "-3", ... appended when taken, the base shortened to keep the id
// within the 64-character limit.
export function uniqueAgentId(name: string, taken: Set<string>): string {
  const base = normalizeAgentId(name, "agent")
  let id = base
  for (let n = 2; taken.has(id); n++) {
    const suffix = `-${n}`
    id = base.slice(0, 64 - suffix.length).replace(/[-_]+$/, "") + suffix
  }
  return id
}

export interface StepDef {
  key: string
  title: string
}
