export type JsonRecord = Record<string, unknown>

export interface CoreConfigForm {
  workspace: string
  restrictToWorkspace: boolean
  allowRemote: boolean
  maxTokens: string
  maxToolIterations: string
  summarizeMessageThreshold: string
  summarizeTokenPercent: string
  sessionMode: string
  devicesEnabled: boolean
  monitorUSB: boolean
}

export interface LauncherForm {
  port: string
  publicAccess: boolean
  allowedCIDRsText: string
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
  workspace: "",
  restrictToWorkspace: true,
  allowRemote: true,
  maxTokens: "32768",
  maxToolIterations: "50",
  summarizeMessageThreshold: "20",
  summarizeTokenPercent: "75",
  sessionMode: "unified",
  devicesEnabled: false,
  monitorUSB: true,
}

export const EMPTY_LAUNCHER_FORM: LauncherForm = {
  port: "18800",
  publicAccess: false,
  allowedCIDRsText: "",
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
  const agents = asRecord(root.agents)
  const defaults = asRecord(agents.defaults)
  const session = asRecord(root.session)
  const devices = asRecord(root.devices)
  const tools = asRecord(root.tools)
  const exec = asRecord(tools.exec)

  return {
    workspace: asString(defaults.workspace) || EMPTY_FORM.workspace,
    restrictToWorkspace:
      defaults.restrict_to_workspace === undefined
        ? EMPTY_FORM.restrictToWorkspace
        : asBool(defaults.restrict_to_workspace),
    allowRemote:
      exec.allow_remote === undefined
        ? EMPTY_FORM.allowRemote
        : asBool(exec.allow_remote),
    maxTokens: asNumberString(defaults.max_tokens, EMPTY_FORM.maxTokens),
    maxToolIterations: asNumberString(
      defaults.max_tool_iterations,
      EMPTY_FORM.maxToolIterations,
    ),
    summarizeMessageThreshold: asNumberString(
      defaults.summarize_message_threshold,
      EMPTY_FORM.summarizeMessageThreshold,
    ),
    summarizeTokenPercent: asNumberString(
      defaults.summarize_token_percent,
      EMPTY_FORM.summarizeTokenPercent,
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
