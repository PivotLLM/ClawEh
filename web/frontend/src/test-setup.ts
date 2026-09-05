import { afterEach, beforeEach, vi } from "vitest"

// Fail a test on any console.error or console.warn.
//
// This is the guard against slow rot. React reports most of its real problems —
// invalid hook usage, missing keys, state updates outside act(), updates to an
// unmounted component, DOM nesting violations — as console.error or
// console.warn and then carries on. Vitest passes regardless, so without this a
// component test can be green while the component under test is complaining.
//
// The spies are installed in beforeEach, not at module scope: vitest is
// configured with restoreMocks, which strips module-scope spies after the first
// test and would leave every later test unguarded.
const LEVELS = ["error", "warn"] as const
type Level = (typeof LEVELS)[number]

let seen: Array<{ level: Level; text: string }> = []
let allowed: RegExp[] = []

/**
 * Allow one expected console message for the rest of the current test. Use it
 * when the code under test is SUPPOSED to log — a caught network error, say —
 * so the output is declared rather than silently tolerated.
 */
export function expectConsole(pattern: RegExp) {
  allowed.push(pattern)
}

beforeEach(() => {
  seen = []
  allowed = []
  for (const level of LEVELS) {
    vi.spyOn(console, level).mockImplementation((...args: unknown[]) => {
      const text = args
        .map((a) => (a instanceof Error ? a.message : String(a)))
        .join(" ")
      if (!allowed.some((re) => re.test(text))) seen.push({ level, text })
    })
  }
})

afterEach(() => {
  const unexpected = seen
  seen = []
  if (unexpected.length === 0) return
  // Thrown rather than asserted with expect(): an expect() outside a test block
  // is itself a lint warning, and throwing fails the test just as clearly.
  throw new Error(
    "unexpected console output during this test:\n" +
      unexpected.map((m) => `  console.${m.level}: ${m.text}`).join("\n") +
      "\n\nIf the output is expected, declare it with expectConsole(/…/).",
  )
})
