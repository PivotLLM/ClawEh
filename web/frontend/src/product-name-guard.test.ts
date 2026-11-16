// @vitest-environment node
/// <reference types="node" />
import { readFileSync, readdirSync, statSync } from "node:fs"
import { join, relative } from "node:path"
import { fileURLToPath } from "node:url"

import { describe, expect, it } from "vitest"

// The WebUI holds no product name: it shows the names the server sends
// (store/product, from /api/auth/status and /api/system/version), so a rename
// is an edit to the server's app package only. This guard fails when a source
// file, a locale string or index.html names the product.
//
// It scans every non-test .ts/.tsx file under src/ (comments removed), every
// locale JSON file, index.html and the text files in public/, for "ClawEh" or "claw" as a word, in any
// case. Before matching, the identifiers below are removed: they are names of
// real things (files, keys, hosts, a repository) that do not change with the
// product's name. To allow another, add its pattern here with a comment saying
// what it names — never the product name used as a label.
const allowed: RegExp[] = [
  /github\.com\/PivotLLM\/ClawEh/g, // the repository URL
  /(use-)?claw-chat(-[a-z]+)?/g, // module names: lib/claw-chat-*, hooks/use-claw-chat
  /claw:[a-z-]+/g, // localStorage key prefix (claw:last-session-id)
  /claw\.setupDismissed/g, // localStorage key
  /claw\.(example\.com|home\.arpa|local)/g, // example host names in placeholders
  /(YYYYMMDD-)?claw\.log/g, // the log file name
  /claw-backup-/g, // backup archive file name
  /claw-auth/g, // the companion OAuth helper binary
]

const productName = /\bclaw(eh)?\b/i

const root = fileURLToPath(new URL("..", import.meta.url))

function walk(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    const p = join(dir, entry)
    if (statSync(p).isDirectory()) walk(p, out)
    else out.push(p)
  }
  return out
}

// stripComments removes block, JSX and line comments; a "//" after ":" is a
// URL, not a comment.
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:])\/\/.*$/gm, "$1")
}

function offenders(file: string, text: string): string[] {
  let cleaned = text
  for (const re of allowed) cleaned = cleaned.replace(re, "")
  return cleaned
    .split("\n")
    .filter((line) => productName.test(line))
    .map((line) => `${relative(root, file)}: ${line.trim()}`)
}

describe("product name guard", () => {
  it("no source, locale string, index.html or public file names the product", () => {
    const sources = walk(join(root, "src")).filter(
      (f) =>
        /\.(ts|tsx)$/.test(f) &&
        !/\.test\.(ts|tsx)$/.test(f) &&
        !f.endsWith(".d.ts"),
    )
    const locales = walk(join(root, "src", "i18n", "locales")).filter((f) =>
      f.endsWith(".json"),
    )
    // Text files served as they are (public/); images are skipped.
    const publicFiles = walk(join(root, "public")).filter((f) =>
      /\.(webmanifest|json|svg|html|txt|xml)$/.test(f),
    )
    const found = [
      ...publicFiles.flatMap((f) => offenders(f, readFileSync(f, "utf8"))),
      ...sources.flatMap((f) =>
        offenders(f, stripComments(readFileSync(f, "utf8"))),
      ),
      ...locales.flatMap((f) => offenders(f, readFileSync(f, "utf8"))),
      ...offenders(
        join(root, "index.html"),
        readFileSync(join(root, "index.html"), "utf8"),
      ),
    ]
    expect(found).toEqual([])
  })

  it("catches the product name used as a label", () => {
    expect(offenders("x.tsx", 'const label = "ClawEh on GitHub"')).toHaveLength(
      1,
    )
    expect(offenders("x.tsx", "<span>Claw</span>")).toHaveLength(1)
    expect(
      offenders("x.tsx", 'import { x } from "@/lib/claw-chat-state"'),
    ).toEqual([])
  })
})
