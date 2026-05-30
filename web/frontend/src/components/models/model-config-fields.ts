import type { TFunction } from "i18next"

// Mirror of pkg/config/config.go reservedRequestBodyKeys. Keep in sync.
export const RESERVED_REQUEST_BODY_KEYS: readonly string[] = [
  "model",
  "messages",
  "stream",
  "tools",
  "tool_choice",
  "parallel_tool_calls",
  "reasoning_effort",
  "temperature",
  "max_tokens",
  "max_completion_tokens",
  "top_p",
  "n",
] as const

export const REASONING_EFFORT_OPTIONS = ["low", "medium", "high"] as const

export type ExtraBodyParseResult =
  | { value: Record<string, unknown> | undefined; error: undefined }
  | { value: undefined; error: string }

// formatExtraBody renders a saved extra_body map as pretty-printed JSON for the
// textarea. An empty / absent map collapses to "" so the field renders blank.
export function formatExtraBody(
  extra: Record<string, unknown> | null | undefined,
): string {
  if (!extra || Object.keys(extra).length === 0) return ""
  return JSON.stringify(extra, null, 2)
}

// parseExtraBody turns the textarea contents into either an extra_body map to
// send (or `undefined` when the field is empty / `{}`), or an inline error
// message. Empty/whitespace/`{}` is treated as "no override" so the JSON field
// is omitted entirely on save.
export function parseExtraBody(
  raw: string,
  t: TFunction,
): ExtraBodyParseResult {
  const trimmed = raw.trim()
  if (trimmed === "" || trimmed === "{}") {
    return { value: undefined, error: undefined }
  }

  let parsed: unknown
  try {
    parsed = JSON.parse(trimmed)
  } catch {
    return { value: undefined, error: t("models.field.extraBodyInvalidJSON") }
  }

  if (
    parsed === null ||
    typeof parsed !== "object" ||
    Array.isArray(parsed)
  ) {
    return { value: undefined, error: t("models.field.extraBodyNotObject") }
  }

  const obj = parsed as Record<string, unknown>
  for (const key of Object.keys(obj)) {
    if (RESERVED_REQUEST_BODY_KEYS.includes(key)) {
      return {
        value: undefined,
        error: t("models.field.extraBodyReservedKey", { key }),
      }
    }
  }

  if (Object.keys(obj).length === 0) {
    return { value: undefined, error: undefined }
  }

  return { value: obj, error: undefined }
}
