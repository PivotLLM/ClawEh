#!/usr/bin/env node
// ClawEh WebUI regression suite.
//
// Executes docs/frontend-test-plan.md. Every check here maps to a numbered step
// in that document; keep the two in step.
//
//   node tests/frontend-e2e.mjs                     # against http://127.0.0.1:8077
//   node tests/frontend-e2e.mjs --base http://host:port
//   node tests/frontend-e2e.mjs --only C,F          # run selected groups
//
// Requires Playwright and a Chromium build. Both come with the playwright-mcp
// install; override with PLAYWRIGHT_MODULE / CHROME_PATH if they live elsewhere.
//
// SAFETY: this mutates configuration, so point it at a DEV instance. Every
// mutation is reverted — the agent it creates is deleted, and every field it
// edits is restored to the value read beforehand. It refuses to run against the
// production port (18790) unless --allow-prod is passed.

import { existsSync } from "node:fs"

const args = process.argv.slice(2)
const arg = (name, fallback) => {
  const i = args.indexOf(name)
  return i >= 0 && args[i + 1] ? args[i + 1] : fallback
}
const BASE = arg("--base", "http://127.0.0.1:8077").replace(/\/$/, "")
const ONLY = arg("--only", "")
  .split(",")
  .filter(Boolean)
  .map((s) => s.toUpperCase())

if (BASE.includes(":18790") && !args.includes("--allow-prod")) {
  console.error(
    `Refusing to run against ${BASE}: that is the production port.\n` +
      `This suite writes configuration. Point it at claw-dev, or pass --allow-prod.`,
  )
  process.exit(2)
}

const PW =
  process.env.PLAYWRIGHT_MODULE ??
  "/home/ai/.npm/_npx/9833c18b2d85bc59/node_modules/playwright/index.mjs"
const CHROME =
  process.env.CHROME_PATH ??
  "/home/eric/.cache/ms-playwright/chromium-1200/chrome-linux64/chrome"

if (!existsSync(CHROME)) {
  console.error(`Chromium not found at ${CHROME}. Set CHROME_PATH.`)
  process.exit(2)
}
const { chromium } = await import(PW)

// ---------------------------------------------------------------- harness ---

const results = []
let browser
let group = ""

function useGroup(id, title) {
  group = id
  if (ONLY.length && !ONLY.includes(id)) return false
  console.log(`\n── ${id}. ${title}`)
  return true
}

async function check(id, what, fn) {
  const label = `${group}${id}`
  try {
    const detail = await fn()
    results.push({ label, what, ok: true })
    console.log(`   PASS ${label}  ${what}${detail ? ` — ${detail}` : ""}`)
  } catch (e) {
    results.push({ label, what, ok: false, error: e.message })
    console.log(`   FAIL ${label}  ${what}`)
    console.log(`        ${e.message.split("\n")[0]}`)
  }
}

function assert(cond, msg) {
  if (!cond) throw new Error(msg)
}

async function api(path, init) {
  const res = await fetch(BASE + path, init)
  const text = await res.text()
  let json
  try {
    json = JSON.parse(text)
  } catch {
    /* not json */
  }
  return { status: res.status, text, json }
}

async function config() {
  const { json } = await api("/api/config")
  return json
}

// Groups F and G write configuration, which the gateway picks up on a debounced
// reload (~15s later) that briefly stops the channels. Anything loading a page
// during that window sees the chat WebSocket handshake fail with 503 and logs a
// console error — a real effect of a reload, not a defect, but it makes later
// groups fail for the wrong reason.
//
// So force the reload now rather than letting it fire unpredictably later, and
// wait for readiness. POST /api/gateway/reload applies immediately and blocks
// until the reload completes; /ready then confirms the channels are back.
async function settleAfterConfigWrites() {
  await api("/api/gateway/reload", { method: "POST" })
  const deadline = Date.now() + 60000
  for (;;) {
    const r = await api("/ready")
    if (r.status === 200) return
    if (Date.now() > deadline) throw new Error("gateway did not become ready after reload")
    await new Promise((res) => setTimeout(res, 500))
  }
}

// Opens a page with console/error capture attached.
async function open(path, { wait = "networkidle" } = {}) {
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  const problems = []
  page.on("console", (m) => {
    if (m.type() === "error") problems.push(m.text())
  })
  page.on("pageerror", (e) => problems.push("pageerror: " + e.message))
  await page.goto(BASE + path, { waitUntil: wait, timeout: 20000 })
  await page.waitForTimeout(500)
  return { ctx, page, problems, text: () => page.locator("body").innerText() }
}

const ROUTES = [
  "/",
  "/agents",
  "/agent/bindings",
  "/agent/tools",
  "/agent/skills",
  "/channels",
  "/config",
  "/config/raw",
  "/devices",
  "/logs",
  "/mcp",
  "/mcp/servers",
  "/memory",
  "/models",
  "/providers",
  "/voice",
  "/setup",
]

// A leaked i18n key: i18next renders the key verbatim when lookup fails.
const I18N_KEY =
  /\b(navigation|pages|setup|channels|labels|agents|models|providers|common)\.[a-z][a-zA-Z0-9_]*(\.[a-zA-Z0-9_]+)*\b/g

// ------------------------------------------------------------------ suite ---

browser = await chromium.launch({ executablePath: CHROME })
console.log(`ClawEh WebUI regression suite — ${BASE}`)

// A. Preconditions
if (useGroup("A", "Preconditions")) {
  await check(1, "gateway is up and READY", async () => {
    // /health answers as soon as the listener binds; /ready only once the
    // channels have started. Poll the latter, or every step below races boot.
    const deadline = Date.now() + 30000
    let last = 0
    for (;;) {
      const r = await api("/ready")
      last = r.status
      if (r.status === 200) return "ready"
      if (Date.now() > deadline) break
      await new Promise((r) => setTimeout(r, 500))
    }
    const h = await api("/health")
    assert(false, `/ready still ${last} after 30s (/health = ${h.status})`)
  })
  await check(2, "version reports release, commit and build number", async () => {
    const r = await api("/api/system/version")
    const v = r.json?.version ?? ""
    assert(/^\d+\.\d+\.\d+/.test(v), `version malformed: ${v}`)
    assert(/\+[0-9a-f]{8}/.test(v), `no commit stamp: ${v}`)
    assert(/\[\d{14}\]/.test(v), `no build number: ${v}`)
    return v
  })
  await check(3, "config API returns an agent list", async () => {
    const c = await config()
    assert(Array.isArray(c?.agents?.list), "agents.list missing")
    assert(c.agents.list.length > 0, "no agents configured — seed the dev instance")
    return `${c.agents.list.length} agents`
  })
}

// B. Every route renders
if (useGroup("B", "Route smoke — every page renders, console clean")) {
  for (const route of ROUTES) {
    await check(
      ROUTES.indexOf(route) + 1,
      `GET ${route}`,
      async () => {
        const { ctx, problems, text } = await open(route)
        const body = await text()
        await ctx.close()
        assert(body.trim().length > 40, `page nearly empty (${body.length} chars)`)
        assert(problems.length === 0, `console errors: ${problems[0]}`)
        return `${body.length} chars`
      },
    )
  }
}

// C. Shell and navigation
if (useGroup("C", "Shell and navigation")) {
  await check(1, "sidebar exposes the primary sections", async () => {
    const { ctx, text } = await open("/agents")
    const body = await text()
    await ctx.close()
    for (const item of ["Chat", "Agents", "Models", "Channels", "Services"]) {
      assert(body.includes(item), `sidebar missing "${item}"`)
    }
  })
  await check(2, "sidebar shows the running version", async () => {
    const { ctx, text } = await open("/agents")
    const body = await text()
    await ctx.close()
    assert(/ClawEh v\d+\.\d+\.\d+/.test(body), "version not shown in sidebar")
  })
  await check(3, "sidebar groups expand and their links navigate client-side", async () => {
    // The top-level sidebar entries are collapsible GROUPS (aria-expanded),
    // not links — clicking one reveals the routes beneath it.
    const { ctx, page } = await open("/agents")
    const group = page.getByRole("button", { name: "Models", exact: true })
    assert(
      (await group.getAttribute("aria-expanded")) !== null,
      "sidebar entry is not a disclosure control",
    )
    await group.click()
    await page.waitForTimeout(400)
    assert(
      (await group.getAttribute("aria-expanded")) === "true",
      "group did not expand",
    )
    await page.locator('a[href="/models"]').first().click()
    await page.waitForTimeout(800)
    const url = page.url()
    await ctx.close()
    assert(url.endsWith("/models"), `expected /models, got ${url}`)
  })
  await check(4, "unknown route renders the app, not a server error", async () => {
    const r = await api("/no-such-route")
    assert(r.status === 200, `expected SPA fallback 200, got ${r.status}`)
  })
}

// D. i18n integrity
if (useGroup("D", "i18n integrity")) {
  await check(1, "no untranslated keys on any route", async () => {
    const leaked = []
    for (const route of ROUTES) {
      const { ctx, text } = await open(route)
      const body = await text()
      await ctx.close()
      const hits = [...new Set(body.match(I18N_KEY) || [])]
      if (hits.length) leaked.push(`${route}: ${hits.join(", ")}`)
    }
    assert(leaked.length === 0, `untranslated keys — ${leaked.join(" | ")}`)
    return `${ROUTES.length} routes clean`
  })
  await check(2, "tool categories all have labels", async () => {
    const { ctx, text } = await open("/agent/tools")
    const body = await text()
    await ctx.close()
    assert(!/categories\./.test(body), "a raw category key is rendered")
  })
}

// E. Chat and the WebSocket token
if (useGroup("E", "Chat and WebSocket auth")) {
  await check(1, "token endpoint issues a token and a ws url", async () => {
    const r = await api("/api/webui/token")
    assert(r.status === 200, `expected 200, got ${r.status}`)
    assert(r.json?.token, "no token issued")
    assert(/^wss?:\/\//.test(r.json?.ws_url ?? ""), "ws_url malformed")
  })
  await check(2, "chat opens its socket with the token as a SUBPROTOCOL, never in the URL", async () => {
    const ctx = await browser.newContext()
    const page = await ctx.newPage()
    const sockets = []
    await page.addInitScript(() => {
      const Real = window.WebSocket
      window.__sockets = []
      // @ts-ignore
      window.WebSocket = function (url, protocols) {
        window.__sockets.push({ url: String(url), protocols })
        return new Real(url, protocols)
      }
      window.WebSocket.prototype = Real.prototype
      Object.assign(window.WebSocket, Real)
    })
    await page.goto(BASE + "/", { waitUntil: "networkidle", timeout: 20000 })
    await page.waitForTimeout(2500)
    const seen = await page.evaluate(() => window.__sockets ?? [])
    sockets.push(...seen)
    const { token } = (await api("/api/webui/token")).json ?? {}
    await ctx.close()
    assert(sockets.length > 0, "chat opened no WebSocket")
    const s = sockets[0]
    assert(!/[?&]token=/.test(s.url), `token found in URL: ${s.url}`)
    if (token) assert(!s.url.includes(token), "token value present in URL")
    assert(
      Array.isArray(s.protocols) && s.protocols[0] === "claw-token",
      `expected claw-token subprotocol, got ${JSON.stringify(s.protocols)}`,
    )
    return `subprotocols ${JSON.stringify(s.protocols?.[0])}`
  })
  await check(3, "chat reaches connected state", async () => {
    const { ctx, page, text } = await open("/")
    await page.waitForTimeout(2500)
    const body = await text()
    await ctx.close()
    assert(!/disconnected|connection error/i.test(body), "chat reports disconnected")
  })
}

// F. Agents: create, edit, autosave, index realignment, delete
if (useGroup("F", "Agents — autosave and list realignment")) {
  const PROBE = "e2e-probe"
  let created = false

  await check(1, "create an agent through the UI", async () => {
    const { ctx, page } = await open("/agents")
    await page.getByRole("button", { name: "Add Agent" }).click()
    await page.waitForTimeout(400)
    await page.getByRole("textbox", { name: /Agent ID/i }).fill(PROBE)
    await page.getByRole("button", { name: "Add", exact: true }).click()
    await page.waitForTimeout(1500)
    await ctx.close()
    const c = await config()
    created = (c.agents.list ?? []).some((a) => a.id === PROBE)
    assert(created, `${PROBE} not present in config after Add`)
  })

  await check(2, "edit a field and confirm the debounced autosave persists it", async () => {
    const { ctx, page } = await open("/agents")
    await page.getByRole("button", { name: PROBE, exact: true }).click()
    await page.waitForTimeout(400)
    await page.locator('input[type=number][placeholder="default"]').first().fill("0.77")
    await page.waitForTimeout(2000)
    await ctx.close()
    const c = await config()
    const a = (c.agents.list ?? []).find((x) => x.id === PROBE)
    assert(a?.temperature === 0.77, `temperature = ${a?.temperature}, expected 0.77`)
  })

  await check(3, "a second field on the same agent also persists (no clobber)", async () => {
    const { ctx, page } = await open("/agents")
    await page.getByRole("button", { name: PROBE, exact: true }).click()
    await page.waitForTimeout(400)
    await page
      .locator('input[placeholder="e.g. fusion, fusion_trello"]')
      .first()
      .fill("fusion, trello")
    await page.waitForTimeout(2000)
    await ctx.close()
    const c = await config()
    const a = (c.agents.list ?? []).find((x) => x.id === PROBE)
    assert(
      JSON.stringify(a?.mcp_tools) === JSON.stringify(["fusion", "trello"]),
      `mcp_tools = ${JSON.stringify(a?.mcp_tools)}`,
    )
    assert(a?.temperature === 0.77, "the earlier edit was clobbered by the second save")
  })

  await check(4, "edit buffers follow the list when indices shift", async () => {
    // The probe sorts among the existing agents; selecting each in turn must
    // show that agent's own values, not its neighbour's.
    const c = await config()
    // The rail renders `agent.name || agent.id`, so an agent with a display
    // name is NOT clickable by its id — selecting "claw" misses the button
    // labelled "Claw".
    const rail = (c.agents.list ?? []).map((a) => a.name || a.id)
    assert(rail.length >= 2, "need at least two agents for this check")
    const { ctx, page } = await open("/agents")
    await page.getByRole("button", { name: PROBE, exact: true }).click()
    await page.waitForTimeout(500)
    const probeTemp = await page
      .locator('input[type=number][placeholder="default"]')
      .first()
      .inputValue()
    const other = rail.find((n) => n !== PROBE)
    await page.getByRole("button", { name: other, exact: true }).click()
    await page.waitForTimeout(500)
    const otherTemp = await page
      .locator('input[type=number][placeholder="default"]')
      .first()
      .inputValue()
    await ctx.close()
    assert(probeTemp === "0.77", `probe showed ${probeTemp}`)
    assert(otherTemp !== "0.77", `"${other}" showed the probe's value (${otherTemp})`)
    return `${PROBE}=0.77, ${other}=${otherTemp || "unset"}`
  })

  await check(5, "delete the agent and confirm it is gone", async () => {
    if (!created) return "skipped, never created"
    const { ctx, page } = await open("/agents")
    await page.getByRole("button", { name: PROBE, exact: true }).click()
    await page.waitForTimeout(400)
    page.on("dialog", (d) => d.accept())
    // By accessible name. It used to be an icon-only button with no name at
    // all, which meant a screen reader announced nothing and this step had to
    // guess at CSS classes — `[class*="destructive"]` matches every shadcn
    // button, since they all carry aria-invalid:*destructive* utilities.
    const trash = page.getByRole("button", { name: new RegExp(`Delete agent ${PROBE}`, "i") })
    assert((await trash.count()) > 0, "no labelled delete control on the agent card")
    await trash.click()
    await page.waitForTimeout(1500)
    await ctx.close()
    const c = await config()
    const still = (c.agents.list ?? []).some((a) => a.id === PROBE)
    if (still) {
      // Fall back to the API so the suite never leaves debris behind.
      const cfg = await config()
      cfg.agents.list = (cfg.agents.list ?? []).filter((a) => a.id !== PROBE)
      await api("/api/config", {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ agents: { list: cfg.agents.list } }),
      })
      throw new Error("UI delete did not remove the agent (cleaned up via API)")
    }
  })
}

// G. Config page
if (useGroup("G", "Config page — load, edit, persist, restore")) {
  let original
  await check(1, "config page renders its sections", async () => {
    const { ctx, text } = await open("/config")
    const body = await text()
    await ctx.close()
    for (const s of ["Service", "Runtime", "Backup", "Devices"]) {
      assert(body.includes(s), `missing section "${s}"`)
    }
  })
  await check(2, "an edited field autosaves", async () => {
    const c = await config()
    original = c?.gateway?.external_url ?? ""
    const { ctx, page } = await open("/config")
    const field = page.locator('input[placeholder^="http"]').first()
    await field.fill("http://e2e-probe.invalid:9999")
    await page.waitForTimeout(2000)
    await ctx.close()
    const after = await config()
    assert(
      after?.gateway?.external_url === "http://e2e-probe.invalid:9999",
      `external_url = ${after?.gateway?.external_url}`,
    )
  })
  await check(3, "restore the original value", async () => {
    const { ctx, page } = await open("/config")
    const field = page.locator('input[placeholder^="http"]').first()
    await field.fill(original)
    await page.waitForTimeout(2000)
    await ctx.close()
    const after = await config()
    assert(
      (after?.gateway?.external_url ?? "") === original,
      `external_url = ${after?.gateway?.external_url}, expected ${original}`,
    )
    return `restored to "${original}"`
  })
  await check(4, "raw config view returns the document", async () => {
    const { ctx, text } = await open("/config/raw")
    const body = await text()
    await ctx.close()
    assert(body.length > 100, "raw config appears empty")
  })
}

// Settle the reload caused by F and G before the read-only groups below.
if (!ONLY.length || ONLY.includes("F") || ONLY.includes("G")) {
  await settleAfterConfigWrites()
}

// H. Channels
if (useGroup("H", "Channels — config form and allow_from typing")) {
  await check(1, "channel list renders the known channels", async () => {
    const { ctx, text } = await open("/channels")
    const body = await text()
    await ctx.close()
    assert(/web/i.test(body), "channel list looks empty")
  })
  await check(2, "a channel config page loads for its param", async () => {
    const { ctx, text } = await open("/channels/slack")
    const body = await text()
    await ctx.close()
    assert(/slack/i.test(body), "slack page did not render its channel")
    assert(!/not found/i.test(body), "slack page reported not found")
  })
  await check(3, "a different param renders a different channel", async () => {
    const { ctx, text } = await open("/channels/telegram")
    const body = await text()
    await ctx.close()
    assert(/telegram/i.test(body), "telegram page did not render")
    assert(!/slack/i.test(body), "telegram page leaked slack content")
  })
  await check(4, "typing a trailing separator in allow_from is not eaten", async () => {
    const { ctx, page } = await open("/channels/discord")
    const field = page.locator('input').filter({ hasNot: page.locator('[type=number]') })
    const allow = page
      .locator("input")
      .filter({ has: page.locator(":scope") })
      .nth(0)
    void field
    void allow
    // Find the allow_from input by its neighbouring label text.
    const inputs = await page.locator("input[type=text], input:not([type])").all()
    let target = null
    for (const i of inputs) {
      const ph = (await i.getAttribute("placeholder")) ?? ""
      if (/allow|user id|\*/i.test(ph)) target = i
    }
    if (!target) {
      await ctx.close()
      return "skipped: allow_from field not found on this channel"
    }
    const before = await target.inputValue()
    await target.fill("111,")
    await page.waitForTimeout(900)
    const after = await target.inputValue()
    await target.fill(before)
    await page.waitForTimeout(900)
    await ctx.close()
    assert(after === "111,", `trailing comma was eaten: field shows "${after}"`)
  })
}

// I. Models and providers
if (useGroup("I", "Models and providers")) {
  await check(1, "models page lists configured models", async () => {
    const { ctx, text } = await open("/models")
    const body = await text()
    await ctx.close()
    const r = await api("/api/models")
    const first = r.json?.models?.[0]?.model_name
    if (first) assert(body.includes(first), `"${first}" not shown on the page`)
  })
  await check(2, "providers page lists configured providers", async () => {
    const { ctx, text } = await open("/providers")
    const body = await text()
    await ctx.close()
    const r = await api("/api/providers")
    const first = r.json?.providers?.[0]?.name
    if (first) assert(body.includes(first), `"${first}" not shown on the page`)
  })
  await check(3, "the add-model sheet opens and closes without error", async () => {
    const { ctx, page, problems } = await open("/models")
    await page.getByRole("button", { name: /Add Model/i }).click()
    await page.waitForTimeout(700)
    const open1 = await page.locator("text=/Provider/i").count()
    await page.keyboard.press("Escape")
    await page.waitForTimeout(400)
    await ctx.close()
    assert(open1 > 0, "sheet did not open")
    assert(problems.length === 0, `console errors: ${problems[0]}`)
  })
}

// J. Devices — the store-open regression
if (useGroup("J", "Devices")) {
  await check(1, "devices page renders", async () => {
    const { ctx, text, problems } = await open("/devices")
    const body = await text()
    await ctx.close()
    assert(/device/i.test(body), "devices page looks empty")
    assert(problems.length === 0, `console errors: ${problems[0]}`)
  })
  await check(2, "/api/devices does not fail under concurrent load", async () => {
    // Regression: the handler opened its own SQLite handle per request and lost
    // a race converting the DB to WAL, giving an intermittent 500.
    const rounds = 12
    const bad = []
    for (let i = 0; i < rounds; i++) {
      const [a, b, c] = await Promise.all([
        api("/api/devices"),
        api("/api/devices/pending"),
        api("/api/devices/pair"),
      ])
      for (const r of [a, b, c]) if (r.status >= 500) bad.push(r.status + " " + r.text.slice(0, 60))
    }
    assert(bad.length === 0, `${bad.length}/${rounds * 3} failed — ${bad[0]}`)
    return `${rounds * 3} requests, 0 failures`
  })
}

// K. Remaining pages with live data
if (useGroup("K", "Logs, MCP, memory, voice")) {
  await check(1, "logs page shows log lines", async () => {
    const { ctx, text } = await open("/logs")
    const body = await text()
    await ctx.close()
    assert(body.length > 500, `logs page thin (${body.length} chars)`)
  })
  await check(2, "mcp config and servers pages render", async () => {
    for (const p of ["/mcp", "/mcp/servers"]) {
      const { ctx, text, problems } = await open(p)
      const body = await text()
      await ctx.close()
      assert(body.length > 80, `${p} nearly empty`)
      assert(problems.length === 0, `${p} console: ${problems[0]}`)
    }
  })
  await check(3, "memory and voice pages render", async () => {
    for (const p of ["/memory", "/voice"]) {
      const { ctx, text, problems } = await open(p)
      const body = await text()
      await ctx.close()
      assert(body.length > 80, `${p} nearly empty`)
      assert(problems.length === 0, `${p} console: ${problems[0]}`)
    }
  })
}

// L. Setup wizard
if (useGroup("L", "Setup wizard")) {
  await check(1, "wizard walks all six steps and reflects the choices", async () => {
    const { ctx, page } = await open("/setup")
    const body0 = await page.locator("body").innerText()
    assert(/Welcome/i.test(body0), "welcome step not shown")

    const next = () => page.getByRole("button", { name: /^Next$/ }).click()
    await next() // network
    await page.waitForTimeout(400)
    const port = page.locator("input[type=number]").first()
    await port.fill("19999")
    await next() // provider
    await page.waitForTimeout(600)

    // Pick the first CLI provider — no API key needed.
    await page.getByRole("combobox").first().click()
    await page.waitForTimeout(400)
    const picked = await page.evaluate(() => {
      const o = [...document.querySelectorAll("[role=option]")].find((x) =>
        /CLI/i.test(x.textContent ?? ""),
      )
      if (o) o.click()
      return o?.textContent?.trim() ?? null
    })
    assert(picked, "no CLI provider offered")
    await page.waitForTimeout(500)

    await next() // model
    await page.waitForTimeout(500)
    await next() // agent
    await page.waitForTimeout(500)
    await page.evaluate(() => {
      const i = document.querySelector("input[type=text], input:not([type])")
      if (i) {
        const set = Object.getOwnPropertyDescriptor(
          window.HTMLInputElement.prototype,
          "value",
        ).set
        set.call(i, "Alice")
        i.dispatchEvent(new Event("input", { bubbles: true }))
      }
    })
    await next() // review
    await page.waitForTimeout(600)

    const review = await page.locator("body").innerText()
    await ctx.close()
    assert(/Review/i.test(review), "review step not reached")
    assert(review.includes("Alice"), "review did not carry the agent name")
    assert(/CLI/i.test(review), "review did not carry the provider")
    return `provider "${picked}", agent "Alice"`
  })
  await check(2, "the wizard is not finished (no config was changed)", async () => {
    const c = await config()
    assert(
      !(c.agents.list ?? []).some((a) => a.id === "alice" || a.name === "Alice"),
      "the wizard appears to have been submitted",
    )
  })
}

// M. API surface
if (useGroup("M", "API surface (curl-equivalent)")) {
  const endpoints = [
    ["/api/system/version", 200],
    ["/api/config", 200],
    ["/api/models", 200],
    ["/api/providers", 200],
    ["/api/agents/tools", 200],
    ["/api/skills", 200],
    ["/api/devices", 200],
    ["/api/devices/pending", 200],
    ["/api/webui/token", 200],
    ["/health", 200],
    ["/ready", 200],
  ]
  for (const [path, want] of endpoints) {
    await check(endpoints.findIndex((e) => e[0] === path) + 1, `GET ${path}`, async () => {
      // /ready is legitimately 503 while a config reload restarts the channels,
      // and groups F and G write config, so a single sample here races that
      // window. Poll briefly: a permanent 503 still fails, a transient one does
      // not. Every other endpoint is checked once.
      if (path === "/ready") {
        const deadline = Date.now() + 20000
        let last = 0
        for (;;) {
          const r = await api(path)
          last = r.status
          if (r.status === 200) return
          if (Date.now() > deadline) break
          await new Promise((res) => setTimeout(res, 500))
        }
        assert(false, `/ready stuck at ${last} for 20s`)
        return
      }
      const r = await api(path)
      assert(r.status === want, `expected ${want}, got ${r.status}`)
    })
  }
  await check(90, "GET /api/config masks credentials", async () => {
    const r = await api("/api/config")
    const raw = r.text
    const leaked = (raw.match(/"api_key":\s*"(?!\*|""|null)[^"]{12,}"/g) ?? []).filter(
      (m) => !/\*{2,}/.test(m),
    )
    assert(leaked.length === 0, `unmasked credential in /api/config: ${leaked[0]}`)
  })
}

// ----------------------------------------------------------------- report ---

await browser.close()

const failed = results.filter((r) => !r.ok)
console.log(`\n${"=".repeat(64)}`)
console.log(`  ${results.length - failed.length}/${results.length} checks passed`)
if (failed.length) {
  console.log(`\n  Failures:`)
  for (const f of failed) console.log(`    ${f.label}  ${f.what}\n      ${f.error}`)
}
console.log(`${"=".repeat(64)}`)
process.exit(failed.length ? 1 : 0)
