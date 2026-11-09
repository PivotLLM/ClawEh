import { describe, expect, it } from "vitest"

import {
  type AgentEntry,
  agentPayload,
  classifiersFromRaw,
  classifyModels,
  parseAgent,
  setTaskModelGrant,
  taskModelGrantFromRaw,
  taskModelsFor,
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

describe("classifyModels", () => {
  it("lists the models ticked Classify, in page order", () => {
    expect(
      classifyModels({
        models: [
          { model: "slow", classify: true },
          { model: "fast", text: true },
          { model: " cheap ", classify: true, stt: true },
        ],
      }),
    ).toEqual(["slow", "cheap"])
  })

  it("reads anything malformed as none", () => {
    for (const v of [undefined, null, 1, "x", {}, { models: "x" }]) {
      expect(classifyModels(v)).toEqual([])
    }
    expect(
      classifyModels({
        models: [null, { classify: true }, { model: "", classify: true }],
      }),
    ).toEqual([])
  })
})

describe("toggleClassifiers", () => {
  const bob = (extra: Partial<AgentEntry> = {}): AgentEntry =>
    parseAgent({ id: "bob", ...extra })

  it("turns on with no model to pick when no model is ticked Classify", () => {
    const on = toggleClassifiers(bob({ models: ["slow"] }), [])
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

  it("turns on with the first Classify model, not the agent's own", () => {
    const on = toggleClassifiers(bob({ models: ["slow"] }), ["fast", "cheap"])
    expect(on.classifiers).toEqual({ enabled: true, model: "fast" })
  })

  it("on then off round-trips to no block at all", () => {
    const a = bob({ models: ["slow"] })
    const on = toggleClassifiers(a, ["fast"])
    expect(on.classifiers).toEqual({ enabled: true, model: "fast" })
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

describe("task model grant", () => {
  const bob = (extra: Partial<AgentEntry> = {}): AgentEntry =>
    parseAgent({ id: "bob", ...extra })

  it("reads only the kinds that are on", () => {
    for (const v of [undefined, null, true, [], {}, { text: "yes" }]) {
      expect(taskModelGrantFromRaw(v)).toBeUndefined()
    }
    expect(
      taskModelGrantFromRaw({ text: true, image: false, stt: true, x: true }),
    ).toEqual({
      text: true,
      stt: true,
    })
  })

  it("turns kinds on and off, dropping the block when none is on", () => {
    const on = setTaskModelGrant(bob(), "image", true)
    expect(agentPayload(on).task_models).toEqual({ image: true })
    const both = setTaskModelGrant(on, "tts", true)
    expect(agentPayload(both).task_models).toEqual({ image: true, tts: true })
    const off = setTaskModelGrant(
      setTaskModelGrant(both, "image", false),
      "tts",
      false,
    )
    expect(off.task_models).toBeUndefined()
    expect(agentPayload(off)).not.toHaveProperty("task_models")
  })

  it("keeps a loaded grant on save", () => {
    const loaded = parseAgent({ id: "bob", task_models: { stt: true } })
    expect(agentPayload(loaded).task_models).toEqual({ stt: true })
  })

  it("lists the models ticked for a use, in page order", () => {
    const tm = {
      models: [
        { model: "a", tts: true },
        { model: "b", text: true },
        { model: " c ", tts: true },
      ],
    }
    expect(taskModelsFor(tm, "tts")).toEqual(["a", "c"])
    expect(taskModelsFor(tm, "image")).toEqual([])
    expect(taskModelsFor(undefined, "tts")).toEqual([])
  })
})
