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
  "OpenRouter Chat": "deepseek/deepseek-v4-flash",
}
// Sentinel for "let the CLI use its own default model" — maps to a model whose
// id is the CLI protocol (e.g. "gemini-cli"), which the provider treats as
// "pass no --model arg".
export const CLI_DEFAULT = "__cli_default__"

export type TestState = "idle" | "testing" | "ok" | "warn" | "fail"

// slugify turns an agent display name into a stable id (lowercase, dash-joined).
export function slugify(name: string): string {
  return (
    name
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "") || "agent"
  )
}

export interface StepDef {
  key: string
  title: string
}
