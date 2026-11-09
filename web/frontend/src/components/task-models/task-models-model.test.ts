import { describe, expect, it } from "vitest"

import { TASK_MODEL_DEFAULTS, type TaskModel } from "@/api/task-models"

import defaults from "../../../../../config/testdata/task_model_defaults.json"
import {
  addModel,
  availableModels,
  modelsFor,
  moveRow,
  parseResultChars,
  setUse,
  taskModelsPayload,
  unsupportedUses,
  usesOf,
} from "./task-models-model"

// config/task_models_test.go checks the same file against
// config.DefaultTaskResultTextChars.
describe("TASK_MODEL_DEFAULTS", () => {
  it("matches the server's task_models defaults", () => {
    expect(TASK_MODEL_DEFAULTS).toEqual(defaults)
  })
})

describe("rows", () => {
  const rows: TaskModel[] = [
    { model: "a", stt: true },
    { model: "b", vision: true, stt: true },
    { model: "c" },
  ]

  it("adds a model once, with nothing ticked", () => {
    expect(addModel(rows, "d")).toEqual([...rows, { model: "d" }])
    expect(addModel(rows, "a")).toBe(rows)
    expect(addModel(rows, "")).toBe(rows)
  })

  it("moves a row one place and ignores moves off the ends", () => {
    expect(moveRow(rows, 1, -1).map((r) => r.model)).toEqual(["b", "a", "c"])
    expect(moveRow(rows, 1, 1).map((r) => r.model)).toEqual(["a", "c", "b"])
    expect(moveRow(rows, 0, -1)).toBe(rows)
    expect(moveRow(rows, 2, 1)).toBe(rows)
  })

  it("lists each use's models in page order", () => {
    expect(modelsFor(rows, "stt")).toEqual(["a", "b"])
    expect(modelsFor(moveRow(rows, 1, -1), "stt")).toEqual(["b", "a"])
    expect(modelsFor(rows, "classify")).toEqual([])
  })

  it("ticks and unticks a use without touching the others", () => {
    const on = setUse(rows[2], "classify", true)
    expect(usesOf(on)).toEqual(["classify"])
    const off = setUse(rows[1], "stt", false)
    expect(usesOf(off)).toEqual(["vision"])
    expect(off).not.toHaveProperty("stt")
  })

  it("offers only the models not on the page", () => {
    const options = ["a", "x", "c", "y"].map((name) => ({
      name,
      enabled: true,
      support: {},
    }))
    expect(availableModels(rows, options)).toEqual(["x", "y"])
  })

  it("names the ticked uses the provider cannot serve", () => {
    const option = {
      name: "b",
      enabled: true,
      support: { stt: "no", vision: "yes", text: "no" } as const,
    }
    expect(unsupportedUses(rows[1], option)).toEqual(["stt"])
    expect(unsupportedUses(rows[1], undefined)).toEqual([])
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
  it("keeps TTS defaults only on a TTS model and leaves defaults out", () => {
    const payload = taskModelsPayload(
      [
        {
          model: "tts",
          tts: true,
          tts_defaults: { voice: " alloy ", format: "" },
        },
        { model: "was-tts", text: true, tts_defaults: { voice: "x" } },
        { model: "blank", tts: true, tts_defaults: { voice: " " } },
      ],
      0,
      false,
    )
    expect(payload).toEqual({
      models: [
        { model: "tts", tts: true, tts_defaults: { voice: "alloy" } },
        { model: "was-tts", text: true },
        { model: "blank", tts: true },
      ],
    })
  })

  it("writes the limit and echo when they are set", () => {
    expect(taskModelsPayload([], 500, true)).toEqual({
      models: [],
      result_text_chars: 500,
      echo_transcription: true,
    })
  })
})

describe("image_api", () => {
  it("is saved only for an Images model on the images API", () => {
    expect(
      taskModelsPayload(
        [
          { model: "a", image: true, image_api: "images" },
          { model: "b", image: true, image_api: "chat" },
          { model: "c", text: true, image_api: "images" },
        ],
        0,
        false,
      ).models,
    ).toEqual([
      { model: "a", image: true, image_api: "images" },
      { model: "b", image: true },
      { model: "c", text: true },
    ])
  })
})
