import { useQuery } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"

import { type SystemStatus, getSystemStatus } from "@/api/system"
import { PageHeader } from "@/components/page-header"

/**
 * environment names the host in one line — "Ubuntu 24.04.4 LTS on amd64".
 *
 * The backend supplies a human OS name where the host offers one and leaves it
 * empty otherwise, so fall back to the Go platform string ("linux on amd64").
 * Identifying the exact distro or Mac model is not worth the machinery.
 */
function environment(data: SystemStatus): string {
  return `${data.os_name || data.os} on ${data.arch}`
}

/** Renders a byte count in the unit a person would quote it in. */
function humanBytes(n: number): string {
  if (n <= 0) return "—"
  const units = ["B", "KB", "MB", "GB"]
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

function Stat({
  label,
  value,
  hint,
  testId,
}: {
  label: string
  value: string | number
  hint?: string
  testId: string
}) {
  return (
    <div
      data-testid={testId}
      className="border-border bg-card rounded-lg border px-4 py-3"
    >
      <div className="text-muted-foreground text-xs">{label}</div>
      <div className="mt-1 text-2xl font-semibold tabular-nums">{value}</div>
      {hint && (
        <div className="text-muted-foreground mt-0.5 text-xs">{hint}</div>
      )}
    </div>
  )
}

export function StatusPage() {
  const { t } = useTranslation()

  // Polled rather than fetched once: uptime and memory are the point of the
  // page, and a figure that stops moving reads as a hung process.
  const { data, isLoading, isError } = useQuery<SystemStatus>({
    queryKey: ["system-status"],
    queryFn: getSystemStatus,
    refetchInterval: 5000,
  })

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.status")} />
      <div className="flex-1 overflow-auto p-4">
        {isLoading ? (
          <div className="text-muted-foreground text-sm">
            {t("labels.loading")}
          </div>
        ) : isError || !data ? (
          <div className="text-destructive text-sm">
            {t("pages.status.load_error")}
          </div>
        ) : (
          <div className="mx-auto max-w-[900px] space-y-6">
            <div
              data-testid="status-grid"
              className="grid grid-cols-2 gap-3 sm:grid-cols-4"
            >
              <Stat
                testId="status-uptime"
                label={t("pages.status.uptime")}
                value={data.uptime}
              />
              <Stat
                testId="status-memory"
                label={t("pages.status.memory")}
                value={humanBytes(data.memory_bytes)}
              />
              <Stat
                testId="status-assistants"
                label={t("pages.status.assistants")}
                value={data.agents}
              />
              <Stat
                testId="status-channels"
                label={t("pages.status.channels")}
                value={data.channels}
              />
            </div>

            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <Stat
                testId="status-models"
                label={t("pages.status.models")}
                value={data.models}
              />
              <Stat
                testId="status-providers"
                label={t("pages.status.providers")}
                value={data.providers}
              />
              <Stat
                testId="status-goroutines"
                label={t("pages.status.goroutines")}
                value={data.goroutines}
              />
              <Stat
                testId="status-pid"
                label={t("pages.status.pid")}
                value={data.pid}
              />
            </div>

            <dl
              data-testid="status-detail"
              className="border-border divide-border divide-y rounded-lg border text-sm"
            >
              {[
                [t("pages.status.version"), data.version],
                [t("pages.status.build"), data.build || "—"],
                [t("pages.status.compiler"), data.go_version || "—"],
                [t("pages.status.environment"), environment(data)],
                [
                  t("pages.status.mcp_host"),
                  data.mcp_host ? t("labels.yes") : t("labels.no"),
                ],
                [
                  t("pages.status.cli_providers"),
                  data.cli_providers ? t("labels.yes") : t("labels.no"),
                ],
              ].map(([k, v]) => (
                <div key={k} className="flex gap-4 px-4 py-2">
                  <dt className="text-muted-foreground w-40 shrink-0">{k}</dt>
                  <dd className="font-mono text-xs break-all">{v}</dd>
                </div>
              ))}
            </dl>
          </div>
        )}
      </div>
    </div>
  )
}
