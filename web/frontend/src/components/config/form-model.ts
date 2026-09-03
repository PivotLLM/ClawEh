export type JsonRecord = Record<string, unknown>

export interface CoreConfigForm {
  // Service (gateway.*) — bind host doubles as the network-access toggle
  // ("0.0.0.0" = on, "127.0.0.1" = off), and is the runtime source of truth for
  // the WebUI/API listener. Saved via /api/config.
  gatewayHost: string
  gatewayPort: string
  gatewayExternalUrl: string
  // IP allowlist (gateway.allowed_cidrs), one CIDR per line or comma-separated.
  // Empty = the private-network default enforced by the backend.
  allowedCIDRsText: string
  baseDir: string
  commonDir: string
  restrictToWorkspace: boolean
  allowRemote: boolean
  streamToolActivity: boolean
  maxTokens: string
  maxToolIterations: string
  requestTimeout: string
  turnTimeout: string
  maxSubagentDepth: string
  // Agent defaults (agents.defaults.models / .temperature) and the default-agent
  // id (agents.list[].default). Consolidated here from the Agents page.
  defaultAgentId: string
  defaultModels: string[]
  defaultTemperature: string
  summarizationModels: string[]
  visionModels: string[]
  summarizationDebugCapture: boolean
  compressNormalPercent: string
  compressSafetyPercent: string
  compressMinPercent: string
  compressMessageThreshold: string
  compressRetainTokenPercent: string
  compressRetainMinMessages: string
  // Age-based compaction. compressTriggerDays fires a compaction once the oldest
  // live message passes it; compressRetainMaxAgeDays is how far back the
  // retained tail is then cut to. Keep the trigger HIGHER than the retain cap —
  // the gap is what stops a session re-compacting on every message.
  compressTriggerDays: string
  compressRetainMaxAgeDays: string
  compressRetainMaxTokens: string
  archiveMessageCount: string
  archiveDays: string
  summaryMaxCount: string
  summaryRetentionDays: string
  evictionEnabled: boolean
  evictionNotifyUser: boolean
  evictionProtectTurns: string
  evictionEvictTurns: string
  evictionBudgetBytes: string
  logRetentionDays: string
  sessionMode: string
  devicesEnabled: boolean
  monitorUSB: boolean
  backupEnabled: boolean
  backupAt: string
  backupRetainDays: string
}

export const SESSION_MODE_OPTIONS = [
  {
    value: "unified",
    labelKey: "pages.config.session_mode_unified",
    labelDefault: "Unified",
    descKey: "pages.config.session_mode_unified_desc",
    descDefault:
      "One shared memory for the entire agent, across all users and channels.",
  },
  {
    value: "per-user",
    labelKey: "pages.config.session_mode_per_user",
    labelDefault: "Per User",
    descKey: "pages.config.session_mode_per_user_desc",
    descDefault: "Each person gets their own private memory.",
  },
  {
    value: "per-platform",
    labelKey: "pages.config.session_mode_per_platform",
    labelDefault: "Per Platform",
    descKey: "pages.config.session_mode_per_platform_desc",
    descDefault: "Each person has a separate memory per platform.",
  },
  {
    value: "per-account",
    labelKey: "pages.config.session_mode_per_account",
    labelDefault: "Per Account",
    descKey: "pages.config.session_mode_per_account_desc",
    descDefault: "Like per-platform, but also separates by bot account.",
  },
] as const

export const EMPTY_FORM: CoreConfigForm = {
  gatewayHost: "127.0.0.1",
  gatewayPort: "18790",
  gatewayExternalUrl: "",
  allowedCIDRsText: "",
  baseDir: "",
  commonDir: "",
  restrictToWorkspace: true,
  allowRemote: true,
  streamToolActivity: false,
  maxTokens: "32768",
  maxToolIterations: "50",
  requestTimeout: "300",
  turnTimeout: "900",
  maxSubagentDepth: "3",
  defaultAgentId: "",
  defaultModels: [],
  defaultTemperature: "",
  summarizationModels: [],
  visionModels: [],
  summarizationDebugCapture: false,
  // Compaction fields use "" for "not configured" so the payload can omit them.
  // 0 is a real value here — it disables a trigger — and must be distinguishable
  // from a box the operator never filled in.
  compressNormalPercent: "",
  compressSafetyPercent: "",
  compressMinPercent: "",
  compressMessageThreshold: "",
  compressRetainTokenPercent: "",
  compressRetainMinMessages: "",
  compressTriggerDays: "",
  compressRetainMaxAgeDays: "",
  compressRetainMaxTokens: "",
  archiveMessageCount: "0",
  archiveDays: "0",
  summaryMaxCount: "0",
  summaryRetentionDays: "0",
  evictionEnabled: true,
  evictionNotifyUser: false,
  evictionProtectTurns: "3",
  evictionEvictTurns: "10",
  evictionBudgetBytes: "0",
  logRetentionDays: "30",
  sessionMode: "unified",
  devicesEnabled: false,
  monitorUSB: true,
  backupEnabled: true,
  backupAt: "03:00",
  backupRetainDays: "30",
}

function asRecord(value: unknown): JsonRecord {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value as JsonRecord
  }
  return {}
}

function asString(value: unknown): string {
  return typeof value === "string" ? value : ""
}

function asStringArray(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return []
  }
  return value.filter((v): v is string => typeof v === "string")
}

function asBool(value: unknown): boolean {
  return value === true
}

function asNumberString(value: unknown, fallback: string): string {
  if (typeof value === "number" && Number.isFinite(value)) {
    return String(value)
  }
  if (typeof value === "string" && value.trim() !== "") {
    return value
  }
  return fallback
}

export function buildFormFromConfig(config: unknown): CoreConfigForm {
  const root = asRecord(config)
  const gateway = asRecord(root.gateway)
  const agents = asRecord(root.agents)
  const defaults = asRecord(agents.defaults)
  // agents.defaults.compression.{trigger,retain}; the flat compress_* keys this
  // replaced are migrated server-side on load, so only the nested form is read.
  const compression = asRecord(defaults.compression)
  const compressionTrigger = asRecord(compression.trigger)
  const compressionRetain = asRecord(compression.retain)
  const summarization = asRecord(root.summarization)
  const session = asRecord(root.session)
  const devices = asRecord(root.devices)
  const logging = asRecord(root.logging)
  const tools = asRecord(root.tools)
  const exec = asRecord(tools.exec)

  const agentList = Array.isArray(agents.list) ? agents.list : []
  const defaultAgentId = asString(
    (
      agentList.find((a) => asRecord(a).default === true) as
        { id?: unknown } | undefined
    )?.id,
  )

  return {
    gatewayHost: asString(gateway.host) || EMPTY_FORM.gatewayHost,
    gatewayPort: asNumberString(gateway.port, EMPTY_FORM.gatewayPort),
    gatewayExternalUrl: asString(gateway.external_url),
    allowedCIDRsText: asStringArray(gateway.allowed_cidrs).join("\n"),
    baseDir: asString(agents.base_dir),
    commonDir: asString(agents.common_dir),
    restrictToWorkspace:
      defaults.restrict_to_workspace === undefined
        ? EMPTY_FORM.restrictToWorkspace
        : asBool(defaults.restrict_to_workspace),
    allowRemote:
      exec.allow_remote === undefined
        ? EMPTY_FORM.allowRemote
        : asBool(exec.allow_remote),
    streamToolActivity:
      defaults.stream_tool_activity === undefined
        ? EMPTY_FORM.streamToolActivity
        : asBool(defaults.stream_tool_activity),
    maxTokens: asNumberString(defaults.max_tokens, EMPTY_FORM.maxTokens),
    maxToolIterations: asNumberString(
      defaults.max_tool_iterations,
      EMPTY_FORM.maxToolIterations,
    ),
    requestTimeout: asNumberString(
      defaults.request_timeout,
      EMPTY_FORM.requestTimeout,
    ),
    turnTimeout: asNumberString(defaults.turn_timeout, EMPTY_FORM.turnTimeout),
    maxSubagentDepth: asNumberString(
      defaults.max_subagent_depth,
      EMPTY_FORM.maxSubagentDepth,
    ),
    defaultAgentId,
    defaultModels: asStringArray(defaults.models),
    defaultTemperature:
      typeof defaults.temperature === "number"
        ? String(defaults.temperature)
        : "",
    summarizationModels: asStringArray(summarization.models),
    // vision_model + vision_model_fallbacks are one ordered chain in the UI:
    // index 0 is vision_model, the rest are the fallbacks.
    visionModels: [
      asString(defaults.vision_model),
      ...asStringArray(defaults.vision_model_fallbacks),
    ].filter(Boolean),
    summarizationDebugCapture: asBool(summarization.debug_capture),
    compressNormalPercent: asNumberString(
      compressionTrigger.normal_percent,
      EMPTY_FORM.compressNormalPercent,
    ),
    compressSafetyPercent: asNumberString(
      compressionTrigger.safety_percent,
      EMPTY_FORM.compressSafetyPercent,
    ),
    compressMinPercent: asNumberString(
      compressionTrigger.min_percent,
      EMPTY_FORM.compressMinPercent,
    ),
    compressMessageThreshold: asNumberString(
      compressionTrigger.message_count,
      EMPTY_FORM.compressMessageThreshold,
    ),
    compressTriggerDays: asNumberString(
      compressionTrigger.days,
      EMPTY_FORM.compressTriggerDays,
    ),
    compressRetainTokenPercent: asNumberString(
      compressionRetain.token_percent,
      EMPTY_FORM.compressRetainTokenPercent,
    ),
    compressRetainMinMessages: asNumberString(
      compressionRetain.min_messages,
      EMPTY_FORM.compressRetainMinMessages,
    ),
    compressRetainMaxAgeDays: asNumberString(
      compressionRetain.max_age_days,
      EMPTY_FORM.compressRetainMaxAgeDays,
    ),
    compressRetainMaxTokens: asNumberString(
      compressionRetain.max_tokens,
      EMPTY_FORM.compressRetainMaxTokens,
    ),
    archiveMessageCount: asNumberString(
      defaults.archive_message_count,
      EMPTY_FORM.archiveMessageCount,
    ),
    archiveDays: asNumberString(defaults.archive_days, EMPTY_FORM.archiveDays),
    summaryMaxCount: asNumberString(
      defaults.summary_max_count,
      EMPTY_FORM.summaryMaxCount,
    ),
    summaryRetentionDays: asNumberString(
      defaults.summary_retention_days,
      EMPTY_FORM.summaryRetentionDays,
    ),
    evictionEnabled: asRecord(defaults.context_eviction).enabled !== false, // on by default
    evictionNotifyUser:
      asRecord(defaults.context_eviction).notify_user === true,
    evictionProtectTurns: asNumberString(
      asRecord(defaults.context_eviction).protect_turns,
      EMPTY_FORM.evictionProtectTurns,
    ),
    evictionEvictTurns: asNumberString(
      asRecord(defaults.context_eviction).evict_turns,
      EMPTY_FORM.evictionEvictTurns,
    ),
    evictionBudgetBytes: asNumberString(
      asRecord(defaults.context_eviction).budget_bytes,
      EMPTY_FORM.evictionBudgetBytes,
    ),
    logRetentionDays: asNumberString(
      logging.retention_days,
      EMPTY_FORM.logRetentionDays,
    ),
    sessionMode: asString(session.mode) || EMPTY_FORM.sessionMode,
    devicesEnabled:
      devices.enabled === undefined
        ? EMPTY_FORM.devicesEnabled
        : asBool(devices.enabled),
    monitorUSB:
      devices.monitor_usb === undefined
        ? EMPTY_FORM.monitorUSB
        : asBool(devices.monitor_usb),
    backupEnabled: asRecord(root.backup).enabled !== false, // on by default
    backupAt: asString(asRecord(root.backup).at) || EMPTY_FORM.backupAt,
    backupRetainDays: asNumberString(
      asRecord(root.backup).retain_days,
      EMPTY_FORM.backupRetainDays,
    ),
  }
}

export function parseIntField(
  rawValue: string,
  label: string,
  options: { min?: number; max?: number } = {},
): number {
  const value = Number(rawValue)
  if (!Number.isInteger(value)) {
    throw new Error(`${label} must be an integer.`)
  }
  if (options.min !== undefined && value < options.min) {
    throw new Error(`${label} must be >= ${options.min}.`)
  }
  if (options.max !== undefined && value > options.max) {
    throw new Error(`${label} must be <= ${options.max}.`)
  }
  return value
}

export function parseCIDRText(raw: string): string[] {
  if (!raw.trim()) {
    return []
  }
  return raw
    .split(/[\n,]/)
    .map((v) => v.trim())
    .filter((v) => v.length > 0)
}

// parseOptionalIntField parses a compaction field that may be left blank.
// Blank means "not configured" and is omitted from the payload so the backend
// default applies; an explicit 0 is a real value (it disables a trigger) and is
// sent through. Without the distinction, opening the config page and saving it
// untouched would silently turn every count- and age-based trigger off.
export function parseOptionalIntField(
  rawValue: string,
  label: string,
  options: { min?: number; max?: number } = {},
): number | undefined {
  if (rawValue.trim() === "") {
    return undefined
  }
  return parseIntField(rawValue, label, options)
}

// nullableInts converts undefined ("not configured") to null for the config
// PATCH endpoint, which is JSON Merge Patch (RFC 7396): an OMITTED key means
// "leave the existing value alone", while an explicit null DELETES it. Blanking
// a field in the UI has to delete the key so the backend default applies again —
// omitting it would silently keep the old value. The save handler persists the
// decoded struct rather than the merged map, so the nulls never reach the file.
export function nullableInts(
  obj: Record<string, number | undefined>,
): Record<string, number | null> {
  const out: Record<string, number | null> = {}
  for (const [k, v] of Object.entries(obj)) {
    out[k] = v === undefined ? null : v
  }
  return out
}
