import { describe, expect, it } from "vitest"

import {
  type AgentEntry,
  agentModelList,
  agentPayload,
  classifiersFromRaw,
  parseAgent,
  toggleClassifiers,
} from "./agent-model"

describe("classifiersFromRaw", () => {
  it("reads anything but an object as absent", () => {
    for (const v of [undefined, null, false, true, 0, "on", [], [{}]]) {
      expect(classifiersFromRaw(v)).toBeUndefined()
    }
  })

  it("reads the switch only when it is true, and a blank model as none", () => {
    expect(classifiersFromRaw({})).toEqual({
      enabled: false,
      model: undefined,
    })
    expect(classifiersFromRaw({ enabled: "true", model: "  " })).toEqual({
      enabled: false,
      model: undefined,
    })
    expect(classifiersFromRaw({ enabled: true, model: " fast " })).toEqual({
      enabled: true,
      model: "fast",
    })
  })
})

describe("agentModelList", () => {
  it("is the agent's own models, else the default models", () => {
    expect(agentModelList({ models: ["slow"] }, ["fast"])).toEqual(["slow"])
    expect(agentModelList({ models: [] }, ["fast"])).toEqual(["fast"])
    expect(agentModelList({}, ["fast", "slow"])).toEqual(["fast", "slow"])
    expect(agentModelList({}, [])).toEqual([])
  })
})

describe("toggleClassifiers", () => {
  const bob = (extra: Partial<AgentEntry> = {}): AgentEntry =>
    parseAgent({ id: "bob", ...extra })

  it("turns on with no model to pick when no model is set anywhere", () => {
    const on = toggleClassifiers(bob(), [])
    expect(on.classifiers).toEqual({ enabled: true, model: undefined })
    // The payload writes the switch without a model key.
    expect(agentPayload(on).classifiers).toEqual({ enabled: true })
  })

  it("does not change the agent it was given", () => {
    const before = bob({ models: ["slow"] })
    const snapshot = structuredClone(before)
    toggleClassifiers(before, ["fast"])
    expect(before).toEqual(snapshot)
  })

  it("on then off round-trips to no block at all", () => {
    const a = bob({ models: ["slow"] })
    const on = toggleClassifiers(a, ["fast"])
    expect(on.classifiers).toEqual({ enabled: true, model: "slow" })
    const off = toggleClassifiers(on, ["fast"])
    expect(off.classifiers).toBeUndefined()
    expect(agentPayload(off)).not.toHaveProperty("classifiers")
  })

  it("a block loaded off is not written back", () => {
    const loadedOff = bob({ classifiers: { enabled: false, model: "slow" } })
    expect(loadedOff.classifiers).toEqual({ enabled: false, model: "slow" })
    expect(agentPayload(loadedOff)).not.toHaveProperty("classifiers")
  })
})
