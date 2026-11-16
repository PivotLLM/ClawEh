// @vitest-environment node
/// <reference types="node" />
import { readFileSync, readdirSync, statSync } from "node:fs"
import { join, relative } from "node:path"
import { fileURLToPath } from "node:url"

import { describe, expect, it } from "vitest"

import en from "./locales/en.json"

// Every literal key passed to t() must exist in en.json; a missing one renders
// as the raw key. Dynamic keys (template strings, variables) are not checked.

const src = fileURLToPath(new URL("..", import.meta.url))

function walk(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    const p = join(dir, entry)
    if (statSync(p).isDirectory()) walk(p, out)
    else out.push(p)
  }
  return out
}

// A key resolves when the path names a string, or a plural form of it does.
function resolves(key: string): boolean {
  const parts = key.split(".")
  const last = parts.pop() as string
  let node: unknown = en
  for (const p of parts) {
    if (typeof node !== "object" || node === null) return false
    node = (node as Record<string, unknown>)[p]
  }
  if (typeof node !== "object" || node === null) return false
  const obj = node as Record<string, unknown>
  return ["", "_one", "_other"].some((s) => typeof obj[last + s] === "string")
}

const call = /(?<![\w.])(?:i18n\.)?t\(\s*(["'])([^"'`\n]+)\1/g

describe("i18n keys", () => {
  it("every literal t() key used in src exists in en.json", () => {
    const files = walk(src).filter(
      (f) => /\.(ts|tsx)$/.test(f) && !/\.test\.(ts|tsx)$/.test(f),
    )
    const missing: string[] = []
    for (const f of files) {
      const text = readFileSync(f, "utf8")
      for (const m of text.matchAll(call)) {
        if (!resolves(m[2])) missing.push(`${relative(src, f)}: ${m[2]}`)
      }
    }
    expect(missing).toEqual([])
  })

  it("resolves plain and plural keys and rejects unknown ones", () => {
    expect(resolves("channels.form.desc.secmsgAccounts")).toBe(true)
    expect(resolves("channels.field.secmsgAccounts")).toBe(false)
    expect(resolves("no.such.key")).toBe(false)
  })
})
