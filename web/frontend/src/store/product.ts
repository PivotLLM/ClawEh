import { atom, getDefaultStore, useAtomValue } from "jotai"

// The product's names come from the server (app package), never from the
// bundle, so a rename is a server-side edit only. The server fills them into
// index.html when it serves it (web/backend/embed.go): the <title> and three
// <meta> tags, read here synchronously at load, so the page has them before
// any request and still has them when the server cannot be reached. The
// /api/auth/status and /api/system/version responses carry them too and may
// refresh them; a response without them never blanks what is known.
//
// Under the Vite dev server index.html is not filled in: the meta tags hold
// their {{…}} placeholders, which are ignored, so the names (and the tab
// title) arrive with the first /api/auth/status response instead.
export interface ProductNames {
  /** Identifies the product: title, footer, report header. */
  name: string
  /** Used in running prose: "Claw is reachable from this host only." */
  shortName: string
  /** What the operator types: "claw admin". */
  binaryName: string
}

/** The <meta name> each name is served in. */
export const productNameMeta: Record<keyof ProductNames, string> = {
  name: "product-name",
  shortName: "product-short-name",
  binaryName: "product-binary-name",
}

/** A meta tag's value, or "" when it is absent or still a placeholder. */
function metaContent(name: string): string {
  if (typeof document === "undefined") return ""
  const v =
    document
      .querySelector(`meta[name="${name}"]`)
      ?.getAttribute("content")
      ?.trim() ?? ""
  return v.startsWith("{{") ? "" : v
}

/** The names the served page carries. */
export function namesFromPage(): ProductNames {
  return {
    name: metaContent(productNameMeta.name),
    shortName: metaContent(productNameMeta.shortName),
    binaryName: metaContent(productNameMeta.binaryName),
  }
}

const initial = namesFromPage()

export const productNamesAtom = atom<ProductNames>(initial)

const store = getDefaultStore()

/**
 * The names as i18next interpolation defaults (i18n/index.ts), so a locale
 * string can say {{name}}, {{shortName}} or {{binaryName}} without each call
 * site passing them. Updated in place: i18next reads it at every t() call;
 * i18n/index.ts re-renders translated components when the names change.
 */
export const productNameVariables: Record<keyof ProductNames, string> = {
  ...initial,
}

/** The fields of an API response that carry the names. */
export interface ProductNameFields {
  name?: string
  short_name?: string
  binary_name?: string
}

/**
 * Records names and sets the document title. A field that is missing or
 * empty leaves the name already known.
 */
export function setProductNames(fields: ProductNameFields) {
  const current = store.get(productNamesAtom)
  const next: ProductNames = {
    name: fields.name || current.name,
    shortName: fields.short_name || current.shortName,
    binaryName: fields.binary_name || current.binaryName,
  }
  Object.assign(productNameVariables, next)
  if (next.name && typeof document !== "undefined") {
    document.title = next.name
  }
  if (
    next.name !== current.name ||
    next.shortName !== current.shortName ||
    next.binaryName !== current.binaryName
  ) {
    store.set(productNamesAtom, next)
  }
}

export function useProductNames(): ProductNames {
  return useAtomValue(productNamesAtom)
}
