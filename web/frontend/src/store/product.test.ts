import { getDefaultStore } from "jotai"
import { describe, expect, it } from "vitest"

import i18n from "@/i18n"

import { productNamesAtom, setProductNames } from "./product"

describe("setProductNames", () => {
  it("fills locale strings through the i18next defaults", () => {
    setProductNames({
      name: "Alice Suite",
      short_name: "Alice",
      binary_name: "alice",
    })
    expect(i18n.t("pages.network.urls.none")).toBe(
      "HTTPS is off — Alice is reachable from this host only.",
    )
  })

  it("keeps known names when a response carries none", () => {
    setProductNames({
      name: "Bob Suite",
      short_name: "Bob",
      binary_name: "bob",
    })
    setProductNames({})
    expect(getDefaultStore().get(productNamesAtom)).toEqual({
      name: "Bob Suite",
      shortName: "Bob",
      binaryName: "bob",
    })
  })
})
