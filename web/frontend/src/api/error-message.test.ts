import { afterEach, describe, expect, it, vi } from "vitest"

import { patchAppConfig } from "./channels"
import { setDefaultModel } from "./models"

// A refused save must reach the operator as the server's sentence, never as
// "API error: 400". The server answers a refusal with JSON {"errors": [...]}.

function answer(status: number, body: string) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: false,
      status,
      statusText: status === 400 ? "Bad Request" : "Error",
      text: async () => body,
    }),
  )
}

function refusal(...errors: string[]) {
  answer(400, JSON.stringify({ status: "validation_error", errors }))
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("a refused configuration save", () => {
  it.each([
    [
      "dangling model",
      'agents.list[main].models: model "Ghost" does not exist',
      'agents.list[main].models: model "Ghost" does not exist.',
    ],
    [
      "human agent",
      "Bob (human) represents a person and can't be a summarization model.",
      "Bob (human) represents a person and can't be a summarization model.",
    ],
    [
      "allowed folder",
      'Alice\'s allowed folder "src" must be an absolute path.',
      'Alice\'s allowed folder "src" must be an absolute path.',
    ],
    [
      "agent id",
      'Agent id "Alice Smith" in bindings may use only letters, digits, - and _; use "Alice-Smith".',
      'Agent id "Alice Smith" in bindings may use only letters, digits, - and _; use "Alice-Smith".',
    ],
  ])("shows the %s sentence", async (_kind, sent, shown) => {
    refusal(sent)
    await expect(patchAppConfig({})).rejects.toThrow(shown)
  })

  it("shows every sentence of several refusals", async () => {
    refusal(
      'Alice\'s allowed folder "src" must be an absolute path.',
      'Bob\'s allowed folder "notes" must be an absolute path.',
    )
    await expect(patchAppConfig({})).rejects.toThrow(
      'Alice\'s allowed folder "src" must be an absolute path. ' +
        'Bob\'s allowed folder "notes" must be an absolute path.',
    )
  })

  it("shows the sentence on a save path other than the config", async () => {
    refusal("Bob (human) represents a person and can't be a default model.")
    await expect(setDefaultModel("Bob (human)")).rejects.toThrow(
      "Bob (human) represents a person and can't be a default model.",
    )
  })
})

describe("other failures", () => {
  it("shows a plain-text answer as it is", async () => {
    answer(400, "Invalid JSON: unexpected end of input")
    await expect(patchAppConfig({})).rejects.toThrow(
      "Invalid JSON: unexpected end of input",
    )
  })

  it("shows a single error field", async () => {
    answer(500, JSON.stringify({ error: "config load failed" }))
    await expect(patchAppConfig({})).rejects.toThrow("config load failed")
  })

  it("falls back to the status for an empty answer", async () => {
    answer(500, "")
    await expect(patchAppConfig({})).rejects.toThrow("API error: 500 Error")
  })
})
