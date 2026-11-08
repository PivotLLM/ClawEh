import { describe, expect, it } from "vitest"

import { EMPTY_FORM, buildFormFromConfig } from "./form-model"

describe("buildFormFromConfig classifiers", () => {
  it("maps every classifiers value to its own field", () => {
    const form = buildFormFromConfig({
      classifiers: {
        processed_days: 1,
        limits: {
          max_rules: 2,
          max_prompt_chars: 3,
          max_item_chars: 4,
          max_items_per_run: 5,
          max_parallel: 6,
          max_activity_hours: 7,
          min_every_seconds: 8,
        },
      },
    })
    expect({
      classifierProcessedDays: form.classifierProcessedDays,
      classifierMaxRules: form.classifierMaxRules,
      classifierMaxPromptChars: form.classifierMaxPromptChars,
      classifierMaxItemChars: form.classifierMaxItemChars,
      classifierMaxItemsPerRun: form.classifierMaxItemsPerRun,
      classifierMaxParallel: form.classifierMaxParallel,
      classifierMaxActivityHours: form.classifierMaxActivityHours,
      classifierMinEverySeconds: form.classifierMinEverySeconds,
    }).toEqual({
      classifierProcessedDays: "1",
      classifierMaxRules: "2",
      classifierMaxPromptChars: "3",
      classifierMaxItemChars: "4",
      classifierMaxItemsPerRun: "5",
      classifierMaxParallel: "6",
      classifierMaxActivityHours: "7",
      classifierMinEverySeconds: "8",
    })
  })

  it("shows 0, negatives, non-numbers and a missing block as blank (the default)", () => {
    const form = buildFormFromConfig({
      classifiers: {
        processed_days: 0,
        limits: {
          max_rules: -3,
          max_prompt_chars: "100",
          max_item_chars: null,
          max_parallel: Number.POSITIVE_INFINITY,
        },
      },
    })
    const keys = [
      "classifierProcessedDays",
      "classifierMaxRules",
      "classifierMaxPromptChars",
      "classifierMaxItemChars",
      "classifierMaxItemsPerRun",
      "classifierMaxParallel",
      "classifierMaxActivityHours",
      "classifierMinEverySeconds",
    ] as const
    const pick = (f: typeof form) =>
      Object.fromEntries(keys.map((k) => [k, f[k]]))
    const blank = Object.fromEntries(keys.map((k) => [k, ""]))
    expect(pick(form)).toEqual(blank)
    expect(pick(buildFormFromConfig({}))).toEqual(blank)
    expect(pick(buildFormFromConfig({ classifiers: [] }))).toEqual(blank)
    expect(pick(EMPTY_FORM)).toEqual(blank)
  })
})
