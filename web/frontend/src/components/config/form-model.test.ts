import { describe, expect, it } from "vitest"

import classifierDefaults from "../../../../../config/testdata/classifier_defaults.json"
import defaults from "../../../../../config/testdata/forum_limit_defaults.json"
import { CLASSIFIER_DEFAULTS, FORUM_LIMIT_DEFAULTS } from "./form-model"

// config/forum_test.go checks the same file against config.DefaultForum*, so
// the System page shows the defaults the server applies.
describe("FORUM_LIMIT_DEFAULTS", () => {
  it("matches the server's forum.limits defaults", () => {
    expect(FORUM_LIMIT_DEFAULTS).toEqual(defaults)
  })
})

// config/classifiers_test.go checks the same file against
// config.DefaultClassifier*.
describe("CLASSIFIER_DEFAULTS", () => {
  it("matches the server's classifiers defaults", () => {
    expect(CLASSIFIER_DEFAULTS).toEqual(classifierDefaults)
  })
})
