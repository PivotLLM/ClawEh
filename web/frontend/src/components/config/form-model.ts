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
    descDefault: "One shared memory for the entire agent, across all users and channels.",
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
  compressNormalPercent: "0",
  compressSafetyPercent: "0",
  compressMinPercent: "0",
  compressMessageThreshold: "0",
  compressRetainTokenPercent: "0",
  compressRetainMinMessages: "0",
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
  const summarization = asRecord(root.summarization)
  const session = asRecord(root.session)
  const devices = asRecord(root.devices)
  const logging = asRecord(root.logging)
  const tools = asRecord(root.tools)
  const exec = asRecord(tools.exec)

  const agentList = Array.isArray(agents.list) ? agents.list : []
  const defaultAgentId = asString(
    (agentList.find((a) => asRecord(a).default === true) as { id?: unknown } | undefined)?.id,
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
    requestTimeout: asNumberString(defaults.request_timeout, EMPTY_FORM.requestTimeout),
    turnTimeout: asNumberString(defaults.turn_timeout, EMPTY_FORM.turnTimeout),
    maxSubagentDepth: asNumberString(
      defaults.max_subagent_depth,
      EMPTY_FORM.maxSubagentDepth,
    ),
    defaultAgentId,
    defaultModels: asStringArray(defaults.models),
    defaultTemperature:
      typeof defaults.temperature === "number" ? String(defaults.temperature) : "",
    summarizationModels: asStringArray(summarization.models),
    // vision_model + vision_model_fallbacks are one ordered chain in the UI:
    // index 0 is vision_model, the rest are the fallbacks.
    visionModels: [
      asString(defaults.vision_model),
      ...asStringArray(defaults.vision_model_fallbacks),
    ].filter(Boolean),
    summarizationDebugCapture: asBool(summarization.debug_capture),
    compressNormalPercent: asNumberString(
      defaults.compress_normal_percent,
      EMPTY_FORM.compressNormalPercent,
    ),
    compressSafetyPercent: asNumberString(
      defaults.compress_safety_percent,
      EMPTY_FORM.compressSafetyPercent,
    ),
    compressMinPercent: asNumberString(
      defaults.compress_min_percent,
      EMPTY_FORM.compressMinPercent,
    ),
    compressMessageThreshold: asNumberString(
      defaults.compress_message_threshold,
      EMPTY_FORM.compressMessageThreshold,
    ),
    compressRetainTokenPercent: asNumberString(
      defaults.compress_retain_token_percent,
      EMPTY_FORM.compressRetainTokenPercent,
    ),
    compressRetainMinMessages: asNumberString(
      defaults.compress_retain_min_messages,
      EMPTY_FORM.compressRetainMinMessages,
    ),
    archiveMessageCount: asNumberString(
      defaults.archive_message_count,
      EMPTY_FORM.archiveMessageCount,
    ),
    archiveDays: asNumberString(
      defaults.archive_days,
      EMPTY_FORM.archiveDays,
    ),
    summaryMaxCount: asNumberString(
      defaults.summary_max_count,
      EMPTY_FORM.summaryMaxCount,
    ),
    summaryRetentionDays: asNumberString(
      defaults.summary_retention_days,
      EMPTY_FORM.summaryRetentionDays,
    ),
    evictionEnabled: asRecord(defaults.context_eviction).enabled !== false, // on by default
    evictionNotifyUser: asRecord(defaults.context_eviction).notify_user === true,
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
