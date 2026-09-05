import {
  IconArrowLeft,
  IconArrowRight,
  IconCheck,
  IconLoader2,
} from "@tabler/icons-react"
import { useNavigate } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { getAgentTools, getAppConfig, patchAppConfig } from "@/api/channels"
import {
  type ModelInfo,
  addModel,
  getModels,
  setDefaultModel,
  updateModel,
} from "@/api/models"
import {
  type ProviderInfo,
  getProviders,
  testProvider,
  updateProvider,
} from "@/api/providers"
import {
  type CLIInfo,
  getSetupStatus,
  listCLIs,
  reloadGateway,
} from "@/api/system"
import { SETUP_DISMISSED_KEY } from "@/components/setup/dismissed"
import { AgentStep } from "@/components/setup/steps/agent-step"
import { ModelStep } from "@/components/setup/steps/model-step"
import { NetworkStep } from "@/components/setup/steps/network-step"
import { ProviderStep } from "@/components/setup/steps/provider-step"
import { ReviewStep } from "@/components/setup/steps/review-step"
import { WelcomeStep } from "@/components/setup/steps/welcome-step"
import {
  CLI_DEFAULT,
  COMMON_PROVIDERS,
  CUSTOM_MODEL,
  RECOMMENDED_MODEL,
  type StepDef,
  type TestState,
  slugify,
} from "@/components/setup/wizard-model"
import { Button } from "@/components/ui/button"

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

  // Network step. Defaults are safe: loopback-only bind, standard port, and the
  // address the user is already reaching the WebUI on as the advertised external
  // URL (inherently reachable from their machine, where claw-auth runs).
  const externalUrlDefault = `${window.location.protocol}//${window.location.host}`
  const [networkAccess, setNetworkAccess] = useState(false)
  const [gatewayPort, setGatewayPort] = useState("18790")
  const [externalUrl, setExternalUrl] = useState(externalUrlDefault)

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

  const selectedCli = selectedProvider
    ? cliFor(selectedProvider.protocol)
    : undefined

  // Recommended model id for the selected provider (shown first + tagged).
  const recommendedModelId = selectedProvider
    ? RECOMMENDED_MODEL[selectedProvider.name]
    : undefined

  const presetModels = useMemo(() => {
    const list = models.filter(
      (m) => selectedProvider && m.provider === selectedProvider.name,
    )
    // Surface the recommended model at the top of the list.
    return [...list].sort((a, b) => {
      const ar = a.model === recommendedModelId ? 0 : 1
      const br = b.model === recommendedModelId ? 0 : 1
      return ar - br
    })
  }, [models, selectedProvider, recommendedModelId])

  const cancel = useCallback(() => {
    try {
      sessionStorage.setItem(SETUP_DISMISSED_KEY, "1")
    } catch {
      // sessionStorage may be unavailable; the redirect simply won't be suppressed.
    }
    void navigate({ to: "/" })
  }, [navigate])

  // Memoised because a useMemo further down depends on it: rebuilt every render
  // it would defeat that memo entirely. Only the translations can change it.
  const steps: StepDef[] = useMemo(
    () => [
      { key: "welcome", title: t("setup.steps.welcome") },
      { key: "network", title: t("setup.steps.network") },
      { key: "provider", title: t("setup.steps.provider") },
      { key: "model", title: t("setup.steps.model") },
      { key: "agent", title: t("setup.steps.agent") },
      { key: "review", title: t("setup.steps.review") },
    ],
    [t],
  )

  // Re-test is required whenever the provider or key changes.
  const resetTest = useCallback(() => {
    setTestState("idle")
    setTestMessage("")
  }, [])

  const handleProviderChange = useCallback(
    (v: string) => {
      setProviderName(v)
      setApiKey("")
      resetTest()
      // CLIs need no specific model — default the choice to the
      // CLI's built-in model so the user can just continue.
      const p = allProviders.find((x) => x.name === v)
      setModelChoice(p?.protocol.endsWith("-cli") ? CLI_DEFAULT : "")
    },
    [allProviders, resetTest],
  )

  const handleApiKeyChange = useCallback(
    (v: string) => {
      setApiKey(v)
      resetTest()
    },
    [resetTest],
  )

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
      } else if (
        /cannot|can't|detection|azure|not be|deployment/i.test(res.message)
      ) {
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
      if (modelChoice === CLI_DEFAULT) {
        // The CLI's built-in model: a model whose id is the CLI protocol
        // sentinel (e.g. "gemini-cli"), which makes the provider pass no
        // --model arg. Reuse a seeded sentinel model if one exists.
        const sentinel = selectedProvider.protocol
        const existing = presetModels.find((m) => m.model === sentinel)
        if (existing) {
          await updateModel(existing.index, { enabled: true })
          defaultName = existing.model_name
        } else {
          const label = `${selectedProvider.name} (default)`
          await addModel({
            model_name: label,
            model: sentinel,
            provider: selectedProvider.name,
            enabled: true,
          })
          defaultName = label
        }
      } else if (modelChoice === CUSTOM_MODEL) {
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
            ? {
                ...a,
                name: agentName.trim(),
                models: [defaultName],
                tools: defaultTools,
              }
            : a,
        )
      } else {
        const existingIds = new Set(rawList.map((a) => String(a.id ?? "")))
        let id = slugify(agentName)
        for (let n = 2; existingIds.has(id); n++)
          id = `${slugify(agentName)}-${n}`
        const newAgent: Record<string, unknown> = {
          id,
          name: agentName.trim(),
          models: [defaultName],
          tools: defaultTools,
        }
        if (defaultIdx < 0) newAgent.default = true
        list = [...rawList, newAgent]
      }

      // Persist the reviewed network defaults alongside the agent list. A blank
      // or invalid port falls back to the standard 18790 so Next never blocks.
      const parsedPort = Number.parseInt(gatewayPort, 10)
      const port =
        Number.isInteger(parsedPort) && parsedPort >= 1 && parsedPort <= 65535
          ? parsedPort
          : 18790
      await patchAppConfig({
        gateway: {
          host: networkAccess ? "0.0.0.0" : "127.0.0.1",
          port,
          external_url: externalUrl.trim(),
        },
        agents: { list },
      })

      // Force an immediate reload and wait for it, so the user lands in a ready
      // app instead of the ~10-15s window where the new config isn't live yet.
      // If the reload endpoint is unavailable, the mtime watcher applies it
      // shortly anyway, so don't block finishing on an error.
      try {
        await reloadGateway()
      } catch {
        // fall through — the watcher will pick up the change
      }

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
    networkAccess,
    gatewayPort,
    externalUrl,
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
                i === step
                  ? "font-medium"
                  : "text-muted-foreground hidden sm:inline"
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
          <WelcomeStep alreadyConfigured={alreadyConfigured} />
        )}

        {steps[step].key === "network" && (
          <NetworkStep
            networkAccess={networkAccess}
            setNetworkAccess={setNetworkAccess}
            gatewayPort={gatewayPort}
            setGatewayPort={setGatewayPort}
            externalUrl={externalUrl}
            setExternalUrl={setExternalUrl}
            externalUrlDefault={externalUrlDefault}
          />
        )}

        {steps[step].key === "provider" && (
          <ProviderStep
            providerName={providerName}
            onProviderChange={handleProviderChange}
            apiProviders={apiProviders}
            cliProviders={cliProviders}
            cliFor={cliFor}
            clis={clis}
            selectedProvider={selectedProvider}
            isCliProvider={isCliProvider}
            selectedCli={selectedCli}
            apiKey={apiKey}
            onApiKeyChange={handleApiKeyChange}
            runTest={runTest}
            testState={testState}
            testMessage={testMessage}
          />
        )}

        {steps[step].key === "model" && (
          <ModelStep
            modelChoice={modelChoice}
            setModelChoice={setModelChoice}
            isCliProvider={isCliProvider}
            selectedProvider={selectedProvider}
            presetModels={presetModels}
            recommendedModelId={recommendedModelId}
            customModel={customModel}
            setCustomModel={setCustomModel}
            customLabel={customLabel}
            setCustomLabel={setCustomLabel}
          />
        )}

        {steps[step].key === "agent" && (
          <AgentStep agentName={agentName} setAgentName={setAgentName} />
        )}

        {steps[step].key === "review" && (
          <ReviewStep
            providerName={providerName}
            modelChoice={modelChoice}
            customLabel={customLabel}
            customModel={customModel}
            agentName={agentName}
            finishError={finishError}
            finishing={finishing}
          />
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
