import {
  IconArrowLeft,
  IconArrowRight,
  IconCheck,
  IconLoader2,
  IconX,
} from "@tabler/icons-react"
import { useNavigate } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { getAgentTools, getAppConfig, patchAppConfig } from "@/api/channels"
import {
  addModel,
  getModels,
  setDefaultModel,
  updateModel,
  type ModelInfo,
} from "@/api/models"
import {
  getProviders,
  testProvider,
  updateProvider,
  type ProviderInfo,
} from "@/api/providers"
import { getSetupStatus, listCLIs, type CLIInfo } from "@/api/system"
import { SETUP_DISMISSED_KEY } from "@/components/setup/dismissed"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

// Providers worth surfacing first in the picker — the rest follow alphabetically.
const COMMON_PROVIDERS = [
  "OpenAI",
  "Anthropic",
  "Google API",
  "OpenRouter Chat",
  "Groq",
  "DeepSeek",
  "Mistral",
  "Ollama",
]

const CUSTOM_MODEL = "__custom__"

type TestState = "idle" | "testing" | "ok" | "warn" | "fail"

// slugify turns an agent display name into a stable id (lowercase, dash-joined).
function slugify(name: string): string {
  return (
    name
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "") || "agent"
  )
}

interface StepDef {
  key: string
  title: string
}

export function SetupWizard() {
  const { t } = useTranslation()
  const navigate = useNavigate()

  // Loaded reference data.
  const [providers, setProviders] = useState<ProviderInfo[]>([])
  const [models, setModels] = useState<ModelInfo[]>([])
  const [clis, setClis] = useState<CLIInfo[]>([])
  const [defaultTools, setDefaultTools] = useState<string[]>([])
  // Whether a usable model already exists — drives the "already configured"
  // warning and whether the wizard reconfigures the default agent in place
  // (fresh install) or appends a new one.
  const [alreadyConfigured, setAlreadyConfigured] = useState(false)
  const [loadError, setLoadError] = useState("")
  const [loading, setLoading] = useState(true)

  // Step cursor.
  const [step, setStep] = useState(0)

  // Provider step.
  const [providerName, setProviderName] = useState("")
  const [apiKey, setApiKey] = useState("")
  const [testState, setTestState] = useState<TestState>("idle")
  const [testMessage, setTestMessage] = useState("")

  // Model step.
  const [modelChoice, setModelChoice] = useState("") // a model_name, or CUSTOM_MODEL
  const [customModel, setCustomModel] = useState("")
  const [customLabel, setCustomLabel] = useState("")

  // Agent step.
  const [agentName, setAgentName] = useState("Assistant")

  // Finish.
  const [finishing, setFinishing] = useState(false)
  const [finishError, setFinishError] = useState("")

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const [provs, mods, tools, cliList, status] = await Promise.all([
          getProviders(),
          getModels(),
          getAgentTools(),
          listCLIs(),
          getSetupStatus(),
        ])
        if (cancelled) return
        setProviders(provs.providers)
        setModels(mods.models)
        setDefaultTools(tools.default_tools ?? [])
        setClis(cliList)
        setAlreadyConfigured(status.has_usable_model)
      } catch (e) {
        if (!cancelled) {
          setLoadError(e instanceof Error ? e.message : "Failed to load")
        }
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  // API-key providers, common ones first.
  const apiProviders = useMemo(() => {
    const list = providers.filter((p) => !p.protocol.endsWith("-cli"))
    const rank = (name: string) => {
      const i = COMMON_PROVIDERS.indexOf(name)
      return i === -1 ? COMMON_PROVIDERS.length : i
    }
    return [...list].sort((a, b) => {
      const r = rank(a.name) - rank(b.name)
      return r !== 0 ? r : a.name.localeCompare(b.name)
    })
  }, [providers])

  // CLI providers are first-class connections too, but only selectable when the
  // binary is detected on PATH (no API key — auth is the CLI's own concern).
  const cliProviders = useMemo(
    () => providers.filter((p) => p.protocol.endsWith("-cli")),
    [providers],
  )

  const allProviders = useMemo(
    () => [...apiProviders, ...cliProviders],
    [apiProviders, cliProviders],
  )

  const selectedProvider = useMemo(
    () => allProviders.find((p) => p.name === providerName),
    [allProviders, providerName],
  )

  const isCliProvider = !!selectedProvider?.protocol.endsWith("-cli")

  const cliFor = useCallback(
    (protocol: string) => clis.find((c) => c.protocol === protocol),
    [clis],
  )

  const selectedCli = selectedProvider ? cliFor(selectedProvider.protocol) : undefined

  const presetModels = useMemo(
    () => models.filter((m) => selectedProvider && m.provider === selectedProvider.name),
    [models, selectedProvider],
  )

  const cancel = useCallback(() => {
    try {
      sessionStorage.setItem(SETUP_DISMISSED_KEY, "1")
    } catch {
      // sessionStorage may be unavailable; the redirect simply won't be suppressed.
    }
    void navigate({ to: "/" })
  }, [navigate])

  const steps: StepDef[] = [
    { key: "welcome", title: t("setup.steps.welcome") },
    { key: "provider", title: t("setup.steps.provider") },
    { key: "model", title: t("setup.steps.model") },
    { key: "agent", title: t("setup.steps.agent") },
    { key: "review", title: t("setup.steps.review") },
  ]

  // Re-test is required whenever the provider or key changes.
  const resetTest = useCallback(() => {
    setTestState("idle")
    setTestMessage("")
  }, [])

  const runTest = useCallback(async () => {
    if (!selectedProvider) return
    setTestState("testing")
    setTestMessage("")
    try {
      const res = await testProvider({
        protocol: selectedProvider.protocol,
        base_url: selectedProvider.base_url,
        api_key: apiKey,
      })
      if (res.ok) {
        setTestState("ok")
      } else if (/cannot|can't|detection|azure|not be|deployment/i.test(res.message)) {
        // Not live-testable (e.g. Azure) — let the user proceed with a warning.
        setTestState("warn")
      } else {
        setTestState("fail")
      }
      setTestMessage(res.message)
    } catch (e) {
      setTestState("fail")
      setTestMessage(e instanceof Error ? e.message : "Test failed")
    }
  }, [selectedProvider, apiKey])

  // Per-step gate for the Next button.
  const canAdvance = useMemo(() => {
    switch (steps[step].key) {
      case "provider":
        if (!selectedProvider) return false
        if (isCliProvider) return !!selectedCli?.installed
        return testState === "ok" || testState === "warn"
      case "model":
        if (modelChoice === CUSTOM_MODEL) return customModel.trim() !== ""
        return modelChoice !== ""
      case "agent":
        return agentName.trim() !== ""
      default:
        return true
    }
  }, [
    steps,
    step,
    selectedProvider,
    isCliProvider,
    selectedCli,
    testState,
    modelChoice,
    customModel,
    agentName,
  ])

  const finish = useCallback(async () => {
    if (!selectedProvider) return
    setFinishing(true)
    setFinishError("")
    try {
      // 1. Persist the API key (skip if untestable/local left blank).
      if (apiKey.trim() !== "") {
        await updateProvider(selectedProvider.index, { api_key: apiKey })
      }

      // 2. Enable the chosen model and capture its name for the default.
      let defaultName = ""
      if (modelChoice === CUSTOM_MODEL) {
        const label = (customLabel.trim() || customModel.trim()).trim()
        await addModel({
          model_name: label,
          model: customModel.trim(),
          provider: selectedProvider.name,
          enabled: true,
        })
        defaultName = label
      } else {
        const preset = presetModels.find((m) => m.model_name === modelChoice)
        if (!preset) throw new Error("Selected model not found")
        await updateModel(preset.index, { enabled: true })
        defaultName = preset.model_name
      }

      // 3. Make it the default model.
      await setDefaultModel(defaultName)

      // 4. Set up the first agent. On a pristine install, reconfigure the seeded
      // default agent in place (keeping its id/workspace) rather than adding a
      // second agent that wouldn't be the default. Otherwise append a new one.
      const cfg = (await getAppConfig()) as Record<string, unknown>
      const agentsCfg = (cfg.agents as Record<string, unknown>) ?? {}
      const rawList = Array.isArray(agentsCfg.list)
        ? (agentsCfg.list as Record<string, unknown>[])
        : []
      const defaultIdx = rawList.findIndex((a) => a.default === true)

      let list: Record<string, unknown>[]
      if (!alreadyConfigured && defaultIdx >= 0) {
        list = rawList.map((a, i) =>
          i === defaultIdx
            ? { ...a, name: agentName.trim(), models: [defaultName], tools: defaultTools }
            : a,
        )
      } else {
        const existingIds = new Set(rawList.map((a) => String(a.id ?? "")))
        let id = slugify(agentName)
        for (let n = 2; existingIds.has(id); n++) id = `${slugify(agentName)}-${n}`
        const newAgent: Record<string, unknown> = {
          id,
          name: agentName.trim(),
          models: [defaultName],
          tools: defaultTools,
        }
        if (defaultIdx < 0) newAgent.default = true
        list = [...rawList, newAgent]
      }

      await patchAppConfig({ agents: { list } })

      toast.success(t("setup.finishedToast"))
      void navigate({ to: "/" })
    } catch (e) {
      setFinishError(e instanceof Error ? e.message : "Setup failed")
    } finally {
      setFinishing(false)
    }
  }, [
    selectedProvider,
    apiKey,
    modelChoice,
    customModel,
    customLabel,
    presetModels,
    agentName,
    defaultTools,
    alreadyConfigured,
    navigate,
    t,
  ])

  if (loading) {
    return (
      <div className="flex h-full items-center justify-center">
        <IconLoader2 className="text-muted-foreground size-6 animate-spin" />
      </div>
    )
  }

  return (
    <div className="mx-auto flex h-full w-full max-w-2xl flex-col px-6 py-10">
      {/* Stepper */}
      <ol className="mb-8 flex items-center gap-2 text-sm">
        {steps.map((s, i) => (
          <li key={s.key} className="flex items-center gap-2">
            <span
              className={
                "flex size-6 items-center justify-center rounded-full border text-xs " +
                (i < step
                  ? "border-primary bg-primary text-primary-foreground"
                  : i === step
                    ? "border-primary text-primary"
                    : "border-border text-muted-foreground")
              }
            >
              {i < step ? <IconCheck className="size-3.5" /> : i + 1}
            </span>
            <span
              className={
                i === step ? "font-medium" : "text-muted-foreground hidden sm:inline"
              }
            >
              {s.title}
            </span>
            {i < steps.length - 1 && (
              <span className="text-muted-foreground/40 mx-1">/</span>
            )}
          </li>
        ))}
      </ol>

      {loadError && (
        <p className="text-destructive mb-4 text-sm">{loadError}</p>
      )}

      <div className="flex-1">
        {steps[step].key === "welcome" && (
          <div className="space-y-3">
            <h1 className="text-2xl font-semibold">{t("setup.welcome.title")}</h1>
            <p className="text-muted-foreground">{t("setup.welcome.body")}</p>
            <ul className="text-muted-foreground list-disc space-y-1 pl-5 text-sm">
              <li>{t("setup.welcome.point1")}</li>
              <li>{t("setup.welcome.point2")}</li>
              <li>{t("setup.welcome.point3")}</li>
            </ul>
            {alreadyConfigured && (
              <div className="rounded-xl border border-amber-500/40 bg-amber-500/10 p-4 text-sm text-amber-700 dark:text-amber-300">
                {t("setup.welcome.alreadyConfigured")}
              </div>
            )}
          </div>
        )}

        {steps[step].key === "provider" && (
          <div className="space-y-5">
            <div className="space-y-1">
              <h1 className="text-xl font-semibold">{t("setup.provider.title")}</h1>
              <p className="text-muted-foreground text-sm">
                {t("setup.provider.body")}
              </p>
            </div>

            <div className="space-y-2">
              <Label>{t("setup.provider.selectLabel")}</Label>
              <Select
                value={providerName}
                onValueChange={(v) => {
                  setProviderName(v)
                  setApiKey("")
                  resetTest()
                }}
              >
                <SelectTrigger>
                  <SelectValue placeholder={t("setup.provider.selectPlaceholder")} />
                </SelectTrigger>
                <SelectContent>
                  {apiProviders.map((p) => (
                    <SelectItem key={p.index} value={p.name}>
                      {p.name}
                    </SelectItem>
                  ))}
                  {cliProviders.map((p) => {
                    const info = cliFor(p.protocol)
                    return (
                      <SelectItem
                        key={p.index}
                        value={p.name}
                        disabled={!info?.installed}
                      >
                        {p.name}
                        {info?.installed
                          ? ` — ${t("setup.provider.cliInstalled")}`
                          : ` — ${t("setup.provider.cliMissing")}`}
                      </SelectItem>
                    )
                  })}
                </SelectContent>
              </Select>
              <p className="text-muted-foreground text-xs">
                {t("setup.provider.cliHint")}
              </p>
            </div>

            {selectedProvider && isCliProvider && (
              <div className="border-border/60 bg-card space-y-1 rounded-xl border p-4 text-sm">
                {selectedCli?.installed ? (
                  <>
                    <p className="flex items-center gap-1 text-emerald-600 dark:text-emerald-400">
                      <IconCheck className="size-4" />
                      {t("setup.provider.cliDetected")}
                    </p>
                    {selectedCli.version && (
                      <p className="text-muted-foreground">{selectedCli.version}</p>
                    )}
                    {selectedCli.path && (
                      <p className="text-muted-foreground font-mono text-xs">
                        {selectedCli.path}
                      </p>
                    )}
                  </>
                ) : (
                  <p className="text-destructive flex items-center gap-1">
                    <IconX className="size-4" />
                    {t("setup.provider.cliNotDetected")}
                  </p>
                )}
              </div>
            )}

            {selectedProvider && !isCliProvider && (
              <>
                {selectedProvider.base_url && (
                  <p className="text-muted-foreground text-xs">
                    {t("setup.provider.endpoint")}: {selectedProvider.base_url}
                  </p>
                )}
                <div className="space-y-2">
                  <Label>{t("setup.provider.keyLabel")}</Label>
                  <Input
                    type="password"
                    value={apiKey}
                    autoComplete="off"
                    placeholder={t("setup.provider.keyPlaceholder")}
                    onChange={(e) => {
                      setApiKey(e.target.value)
                      resetTest()
                    }}
                  />
                </div>

                <div className="flex items-center gap-3">
                  <Button
                    variant="outline"
                    onClick={runTest}
                    disabled={testState === "testing"}
                  >
                    {testState === "testing" ? (
                      <IconLoader2 className="size-4 animate-spin" />
                    ) : null}
                    {t("setup.provider.testButton")}
                  </Button>
                  {testState === "ok" && (
                    <span className="flex items-center gap-1 text-sm text-emerald-600 dark:text-emerald-400">
                      <IconCheck className="size-4" /> {testMessage}
                    </span>
                  )}
                  {testState === "warn" && (
                    <span className="text-muted-foreground text-sm">{testMessage}</span>
                  )}
                  {testState === "fail" && (
                    <span className="text-destructive flex items-center gap-1 text-sm">
                      <IconX className="size-4" /> {testMessage}
                    </span>
                  )}
                </div>
              </>
            )}
          </div>
        )}

        {steps[step].key === "model" && (
          <div className="space-y-5">
            <div className="space-y-1">
              <h1 className="text-xl font-semibold">{t("setup.model.title")}</h1>
              <p className="text-muted-foreground text-sm">{t("setup.model.body")}</p>
            </div>

            <div className="space-y-2">
              <Label>{t("setup.model.selectLabel")}</Label>
              <Select value={modelChoice} onValueChange={setModelChoice}>
                <SelectTrigger>
                  <SelectValue placeholder={t("setup.model.selectPlaceholder")} />
                </SelectTrigger>
                <SelectContent>
                  {presetModels.map((m) => (
                    <SelectItem key={m.index} value={m.model_name}>
                      {m.model_name} ({m.model})
                    </SelectItem>
                  ))}
                  <SelectItem value={CUSTOM_MODEL}>
                    {t("setup.model.customOption")}
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>

            {modelChoice === CUSTOM_MODEL && (
              <div className="grid gap-3 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label>{t("setup.model.customIdLabel")}</Label>
                  <Input
                    value={customModel}
                    placeholder="gpt-4o"
                    onChange={(e) => setCustomModel(e.target.value)}
                  />
                </div>
                <div className="space-y-2">
                  <Label>{t("setup.model.customNameLabel")}</Label>
                  <Input
                    value={customLabel}
                    placeholder={t("setup.model.customNamePlaceholder")}
                    onChange={(e) => setCustomLabel(e.target.value)}
                  />
                </div>
              </div>
            )}
          </div>
        )}

        {steps[step].key === "agent" && (
          <div className="space-y-5">
            <div className="space-y-1">
              <h1 className="text-xl font-semibold">{t("setup.agent.title")}</h1>
              <p className="text-muted-foreground text-sm">{t("setup.agent.body")}</p>
            </div>
            <div className="space-y-2">
              <Label>{t("setup.agent.nameLabel")}</Label>
              <Input
                value={agentName}
                onChange={(e) => setAgentName(e.target.value)}
              />
            </div>
          </div>
        )}

        {steps[step].key === "review" && (
          <div className="space-y-5">
            <div className="space-y-1">
              <h1 className="text-xl font-semibold">{t("setup.review.title")}</h1>
              <p className="text-muted-foreground text-sm">{t("setup.review.body")}</p>
            </div>
            <dl className="border-border/60 divide-border/60 divide-y rounded-xl border text-sm">
              <div className="flex justify-between p-3">
                <dt className="text-muted-foreground">{t("setup.steps.provider")}</dt>
                <dd className="font-medium">{providerName}</dd>
              </div>
              <div className="flex justify-between p-3">
                <dt className="text-muted-foreground">{t("setup.steps.model")}</dt>
                <dd className="font-medium">
                  {modelChoice === CUSTOM_MODEL
                    ? customLabel.trim() || customModel.trim()
                    : modelChoice}
                </dd>
              </div>
              <div className="flex justify-between p-3">
                <dt className="text-muted-foreground">{t("setup.steps.agent")}</dt>
                <dd className="font-medium">{agentName.trim()}</dd>
              </div>
            </dl>
            {finishError && (
              <p className="text-destructive text-sm">{finishError}</p>
            )}
          </div>
        )}
      </div>

      {/* Footer nav */}
      <div className="mt-8 flex items-center justify-between border-t pt-4">
        {step === 0 ? (
          <Button variant="ghost" onClick={cancel} disabled={finishing}>
            {t("setup.cancel")}
          </Button>
        ) : (
          <Button
            variant="ghost"
            onClick={() => setStep((s) => Math.max(0, s - 1))}
            disabled={finishing}
          >
            <IconArrowLeft className="size-4" /> {t("setup.back")}
          </Button>
        )}

        {steps[step].key === "review" ? (
          <Button onClick={finish} disabled={finishing}>
            {finishing ? <IconLoader2 className="size-4 animate-spin" /> : null}
            {t("setup.finish")}
          </Button>
        ) : (
          <Button
            onClick={() => setStep((s) => Math.min(steps.length - 1, s + 1))}
            disabled={!canAdvance}
          >
            {t("setup.next")} <IconArrowRight className="size-4" />
          </Button>
        )}
      </div>
    </div>
  )
}
