import { atom, getDefaultStore, useAtomValue } from "jotai"

// The product's names come from the server (app package), never from the
// bundle, so a rename is a server-side edit only. They arrive with
// /api/auth/status, which the auth gate and the login page already fetch, so
// they are known before login and need no request of their own.
export interface ProductNames {
  /** Identifies the product: title, footer, report header. */
  name: string
  /** Used in running prose: "Claw is reachable from this host only." */
  shortName: string
  /** What the operator types: "claw admin". */
  binaryName: string
}

export const productNamesAtom = atom<ProductNames>({
  name: "",
  shortName: "",
  binaryName: "",
})

const store = getDefaultStore()

/**
 * The names as i18next interpolation defaults (i18n/index.ts), so a locale
 * string can say {{name}}, {{shortName}} or {{binaryName}} without each call
 * site passing them. Updated in place: i18next reads it at every t() call.
 */
export const productNameVariables: Record<keyof ProductNames, string> = {
  name: "",
  shortName: "",
  binaryName: "",
}

/** The fields of an API response that carry the names. */
export interface ProductNameFields {
  name?: string
  short_name?: string
  binary_name?: string
}

/**
 * Records the names from an API response and sets the document title. A
 * response without them (an older server) leaves what is already known.
 */
export function setProductNames(fields: ProductNameFields) {
  const current = store.get(productNamesAtom)
  const next: ProductNames = {
    name: fields.name || current.name,
    shortName: fields.short_name || current.shortName,
    binaryName: fields.binary_name || current.binaryName,
  }
  store.set(productNamesAtom, next)
  Object.assign(productNameVariables, next)
  if (next.name && typeof document !== "undefined") {
    document.title = next.name
  }
}

export function useProductNames(): ProductNames {
  return useAtomValue(productNamesAtom)
}
