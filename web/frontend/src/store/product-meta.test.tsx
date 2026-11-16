import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

// The server fills the product names into index.html's meta tags; the store
// reads them when it loads, so the page names the product before (and
// without) any API call. Each test loads fresh modules after setting the tags.

function setMeta(names: Record<string, string>) {
  document.head.querySelectorAll("meta[name^=product-]").forEach((m) => {
    m.remove()
  })
  for (const [name, content] of Object.entries(names)) {
    const m = document.createElement("meta")
    m.setAttribute("name", name)
    m.setAttribute("content", content)
    document.head.appendChild(m)
  }
}

const served = {
  "product-name": "Alice Suite",
  "product-short-name": "Alice",
  "product-binary-name": "alice-cli",
}

beforeEach(() => {
  vi.resetModules()
})

afterEach(() => {
  setMeta({})
  vi.unstubAllGlobals()
})

describe("product names from the served page", () => {
  it("are known at load, before any request", async () => {
    setMeta(served)
    const { namesFromPage } = await import("./product")
    expect(namesFromPage()).toEqual({
      name: "Alice Suite",
      shortName: "Alice",
      binaryName: "alice-cli",
    })
  })

  it("ignores placeholders the dev server leaves unfilled", async () => {
    setMeta({
      "product-name": "{{PRODUCT_NAME}}",
      "product-short-name": "{{PRODUCT_SHORT_NAME}}",
      "product-binary-name": "{{PRODUCT_BINARY_NAME}}",
    })
    const { namesFromPage } = await import("./product")
    expect(namesFromPage()).toEqual({ name: "", shortName: "", binaryName: "" })
  })

  it("name the product on the login page when the server is unreachable", async () => {
    setMeta(served)
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new Error("offline")
      }),
    )
    await import("@/i18n")
    const { LoginPage } = await import("@/components/auth/login-page")
    render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <LoginPage />
      </QueryClientProvider>,
    )
    expect(screen.getByRole("heading").textContent).toBe("Alice Suite")
    await screen.findByTestId("login-unreachable")
    expect(
      screen.getByText("The server could not be reached. Is Alice running?"),
    ).toBeTruthy()
  })

  it("re-render translated sentences when the names change", async () => {
    setMeta(served)
    const i18n = (await import("@/i18n")).default
    const { setProductNames } = await import("./product")
    const { useTranslation } = await import("react-i18next")
    function Sentence() {
      const { t } = useTranslation(undefined, { i18n })
      return <p>{t("pages.network.urls.none")}</p>
    }
    render(<Sentence />)
    expect(
      screen.getByText(
        "HTTPS is off — Alice is reachable from this host only.",
      ),
    ).toBeTruthy()

    act(() => {
      setProductNames({ short_name: "Bob" })
    })
    expect(
      screen.getByText("HTTPS is off — Bob is reachable from this host only."),
    ).toBeTruthy()
  })

  it("are never blanked by a response without them", async () => {
    setMeta(served)
    const { setProductNames, productNamesAtom } = await import("./product")
    const { getDefaultStore } = await import("jotai")
    setProductNames({ name: "", short_name: undefined })
    expect(getDefaultStore().get(productNamesAtom).name).toBe("Alice Suite")
    expect(document.title).toBe("Alice Suite")
  })
})
