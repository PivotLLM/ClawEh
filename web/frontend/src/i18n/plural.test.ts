import i18n from "i18next"
import { describe, expect, it } from "vitest"

import en from "./locales/en.json"

// i18next chooses a plural form by CLDR category, so English wants `_one` /
// `_other`. A `_plural` sibling — the pre-v21 convention — is not an error and
// not a warning: it is simply never selected, and every count renders the
// singular. "2 model" and "1 of 2 model on" both shipped that way.
describe("plural keys", () => {
  it("uses the suffix this i18next actually selects", async () => {
    const inst = i18n.createInstance()
    await inst.init({ lng: "en", resources: { en: { translation: en } } })

    expect(inst.t("providers.modelCount", { count: 1 })).toBe("1 model")
    expect(inst.t("providers.modelCount", { count: 3 })).toBe("3 models")
    expect(inst.t("providers.cli.modelCount", { count: 1, enabled: 1 })).toBe(
      "1 of 1 model on",
    )
    expect(inst.t("providers.cli.modelCount", { count: 3, enabled: 2 })).toBe(
      "2 of 3 models on",
    )
  })

  it("has no _plural keys left, since none of them are ever selected", () => {
    const found: string[] = []
    const walk = (node: unknown, path: string) => {
      if (typeof node !== "object" || node === null) return
      for (const [k, v] of Object.entries(node)) {
        if (k.endsWith("_plural")) found.push(`${path}${k}`)
        walk(v, `${path}${k}.`)
      }
    }
    walk(en, "")
    expect(found).toEqual([])
  })
})
