import { describe, expect, it } from "vitest"

import {
  applyMaestroEdits,
  maestroEditsFromAgent,
  maestroFromRaw,
  maestroPayload,
} from "./agent-model"

describe("maestro block", () => {
  it("parses the object form and drops invalid values", () => {
    expect(
      maestroFromRaw({
        enabled: true,
        max_concurrent: 3,
        rate_limit_requests: 0,
        rate_limit_period: "x",
        allow_parallel: false,
      }),
    ).toEqual({
      enabled: true,
      max_concurrent: 3,
      rate_limit_requests: undefined,
      rate_limit_period: undefined,
      allow_parallel: false,
    })
  })

  it("treats the retired boolean form as a disabled block", () => {
    expect(maestroFromRaw(true)).toEqual({ enabled: false })
    expect(maestroFromRaw(false)).toEqual({ enabled: false })
    expect(maestroFromRaw(undefined)).toBeUndefined()
    expect(maestroFromRaw(null)).toBeUndefined()
  })

  it("sends enabled always and the rest only when set", () => {
    expect(maestroPayload({ enabled: true })).toEqual({ enabled: true })
    expect(
      maestroPayload({
        enabled: false,
        max_concurrent: 2,
        rate_limit_requests: 4,
        rate_limit_period: 30,
        allow_parallel: false,
      }),
    ).toEqual({
      enabled: false,
      max_concurrent: 2,
      rate_limit_requests: 4,
      rate_limit_period: 30,
      allow_parallel: false,
    })
    // allow_parallel true is the default and stays implicit.
    expect(maestroPayload({ enabled: true, allow_parallel: true })).toEqual({
      enabled: true,
    })
  })

  it("round-trips runner edits into the block without touching enabled", () => {
    const agent = {
      id: "a",
      maestro: { enabled: true, max_concurrent: 2, allow_parallel: false },
    }
    const edits = maestroEditsFromAgent(agent)
    expect(edits).toEqual({
      maxConcurrent: 2,
      rateLimitRequests: undefined,
      rateLimitPeriod: undefined,
      allowParallel: false,
    })
    expect(
      applyMaestroEdits(agent.maestro, {
        ...edits,
        allowParallel: true,
        rateLimitPeriod: 90,
      }),
    ).toEqual({
      enabled: true,
      max_concurrent: 2,
      rate_limit_requests: undefined,
      rate_limit_period: 90,
      allow_parallel: undefined,
    })
    // No block: edits never create one (the enabled switch does).
    expect(applyMaestroEdits(undefined, edits)).toBeUndefined()
  })
})
