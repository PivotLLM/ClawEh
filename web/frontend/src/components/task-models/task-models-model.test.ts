import { describe, expect, it } from "vitest"

import {
  TASK_MODEL_DEFAULTS,
  TASK_USES,
  type TaskModelEntry,
  emptyLists,
} from "@/api/task-models"

import defaults from "../../../../../config/testdata/task_model_defaults.json"
import {
  EMPTY_NOTES,
  LIST_DESCRIPTIONS,
  LIST_TITLES,
  addModel,
  availableModels,
  moveRow,
  parseResultChars,
  removeRow,
  taskModelsPayload,
  unsupported,
} from "./task-models-model"

// config/task_models_test.go checks the same file against
// config.DefaultTaskResultTextChars and config.TaskUses.
describe("TASK_MODEL_DEFAULTS", () => {
  it("matches the server's task_models defaults and list order", () => {
    expect(TASK_MODEL_DEFAULTS).toEqual(defaults)
    expect([...TASK_USES]).toEqual(defaults.lists)
  })
})

describe("list labels", () => {
  it("names every list, in sentence case, with a description", () => {
    expect(TASK_USES.map((u) => LIST_TITLES[u])).toEqual([
      "Generate text",
      "Generate images",
      "Text to speech",
      "Transcription",
      "Describe images",
      "Classification",
    ])
    for (const u of TASK_USES) {
      expect(LIST_DESCRIPTIONS[u]).toMatch(/^[A-Z].*\.$/)
      expect(EMPTY_NOTES[u]).toMatch(/^No models\./)
    }
    expect(EMPTY_NOTES.stt).toBe(
      "No models. Voice messages are not transcribed.",
    )
  })
})

describe("a list", () => {
  const list: TaskModelEntry[] = [
    { model: "a" },
    { model: "b", image_api: "images" },
    { model: "c" },
  ]

  it("adds a model once, with no settings", () => {
    expect(addModel(list, "d")).toEqual([...list, { model: "d" }])
    expect(addModel(list, "a")).toBe(list)
    expect(addModel(list, "")).toBe(list)
  })

  it("moves an entry one place, keeping its settings, and ignores moves off the ends", () => {
    expect(moveRow(list, 1, -1)).toEqual([
      { model: "b", image_api: "images" },
      { model: "a" },
      { model: "c" },
    ])
    expect(moveRow(list, 1, 1).map((e) => e.model)).toEqual(["a", "c", "b"])
    expect(moveRow(list, 0, -1)).toBe(list)
    expect(moveRow(list, 2, 1)).toBe(list)
  })

  it("removes an entry", () => {
    expect(removeRow(list, 1).map((e) => e.model)).toEqual(["a", "c"])
  })

  it("offers only the enabled models not in this list", () => {
    const options = [
      { name: "a", enabled: true, support: {} },
      { name: "x", enabled: true, support: {} },
      { name: "off", enabled: false, support: {} },
      { name: "c", enabled: true, support: {} },
      { name: "y", enabled: true, support: {} },
    ]
    expect(availableModels(list, options)).toEqual(["x", "y"])
    // Another list is offered the same models it does not hold itself.
    expect(availableModels([{ model: "x" }], options)).toEqual(["a", "c", "y"])
  })

  it("offers the models sorted by name, ignoring case", () => {
    const options = ["or zeta", "Bob", "alice", "OR Alpha"].map((name) => ({
      name,
      enabled: true,
      support: {},
    }))
    expect(availableModels([], options)).toEqual([
      "alice",
      "Bob",
      "OR Alpha",
      "or zeta",
    ])
  })

  it("says when the provider cannot serve the list's use", () => {
    const option = {
      name: "b",
      enabled: true,
      support: { stt: "no", vision: "yes" } as const,
    }
    expect(unsupported("stt", option)).toBe(true)
    expect(unsupported("vision", option)).toBe(false)
    expect(unsupported("image", option)).toBe(false)
    expect(unsupported("stt", undefined)).toBe(false)
  })
})

describe("parseResultChars", () => {
  it("reads blank as the default and refuses anything but a whole number", () => {
    expect(parseResultChars("")).toBe(0)
    expect(parseResultChars(" 1200 ")).toBe(1200)
    expect(parseResultChars("-1")).toBeNull()
    expect(parseResultChars("1.5")).toBeNull()
    expect(parseResultChars("lots")).toBeNull()
  })
})

describe("taskModelsPayload", () => {
  it("writes every list in order, each entry with only its list's settings", () => {
    const lists = emptyLists()
    lists.text = [{ model: "b" }, { model: "a", image_api: "images" }]
    lists.image = [
      { model: "a", image_api: "images" },
      { model: "b", image_api: "chat" },
    ]
    lists.tts = [
      { model: "tts", tts_defaults: { voice: " alloy ", format: "" } },
      { model: "blank", tts_defaults: { voice: " " } },
    ]
    lists.stt = [{ model: "a", tts_defaults: { voice: "x" } }]
    expect(taskModelsPayload(lists, 0, false)).toEqual({
      text: [{ model: "b" }, { model: "a" }],
      image: [{ model: "a", image_api: "images" }, { model: "b" }],
      tts: [
        { model: "tts", tts_defaults: { voice: "alloy" } },
        { model: "blank" },
      ],
      stt: [{ model: "a" }],
      vision: [],
      classify: [],
    })
  })

  it("writes the limit and echo when they are set", () => {
    expect(taskModelsPayload(emptyLists(), 500, true)).toEqual({
      ...emptyLists(),
      result_text_chars: 500,
      echo_transcription: true,
    })
  })
})
