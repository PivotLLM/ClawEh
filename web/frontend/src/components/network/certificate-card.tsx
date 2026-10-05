import { useState } from "react"
import { useTranslation } from "react-i18next"

import { patchAppConfig } from "@/api/channels"
import { type TLSStatus, regenerateTLS, validateTLSFiles } from "@/api/tls"
import { ConfigSectionCard } from "@/components/config/sections/section-card"
import { Field } from "@/components/shared-form"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"

type CertSource = "self-signed" | "file"

interface CertificateCardProps {
  tls: TLSStatus | undefined
  /** Why GET /api/tls failed, when it did. */
  tlsError: string | null
  /** Called after the certificate or its configuration changed on the server. */
  onChanged: () => void
}

function formatExpiry(notAfter: string): string {
  const d = new Date(notAfter)
  return Number.isNaN(d.getTime()) ? notAfter : d.toLocaleString()
}

// CertificateCard shows the certificate the HTTPS listener presents and lets
// the operator switch between the generated one and their own files. The file
// paths are saved only after POST /api/tls/validate has loaded them, so a typo
// can never leave the listener without a certificate.
export function CertificateCard({
  tls,
  tlsError,
  onChanged,
}: CertificateCardProps) {
  const { t } = useTranslation()

  // The choice and the paths follow the server until the operator edits them.
  // Adjusted during render, not in an effect, so a refetch never paints the
  // previous values for a frame.
  const [source, setSource] = useState<CertSource>("self-signed")
  const [certFile, setCertFile] = useState("")
  const [keyFile, setKeyFile] = useState("")
  const [synced, setSynced] = useState<TLSStatus | undefined>(undefined)
  if (tls && tls !== synced) {
    setSynced(tls)
    setSource(tls.source)
    setCertFile(tls.cert_file)
    setKeyFile(tls.key_file)
  }

  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const run = async (action: () => Promise<void>, done: string) => {
    setBusy(true)
    setError(null)
    setNotice(null)
    try {
      await action()
      setNotice(done)
      onChanged()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  // Validate first; PATCH only when the server could load the pair.
  const saveFiles = () =>
    run(async () => {
      const files = { cert_file: certFile.trim(), key_file: keyFile.trim() }
      await validateTLSFiles(files)
      await patchAppConfig({ gateway: { tls: files } })
    }, t("pages.network.certificate.saved"))

  const useSelfSigned = () =>
    run(async () => {
      await patchAppConfig({
        gateway: { tls: { cert_file: "", key_file: "" } },
      })
    }, t("pages.network.certificate.self_signed_saved"))

  const regenerate = () =>
    run(async () => {
      await regenerateTLS()
    }, t("pages.network.certificate.regenerated"))

  const cert = tls?.certificate

  return (
    <ConfigSectionCard
      title={t("pages.network.certificate.title")}
      description={t("pages.network.certificate.desc")}
    >
      {/* What the listener presents right now. */}
      <div className="py-4" data-testid="cert-current">
        {tlsError ? (
          <p className="text-destructive text-sm">
            {t("pages.network.urls.unavailable", { message: tlsError })}
          </p>
        ) : cert?.present ? (
          <dl className="grid gap-x-6 gap-y-2 text-sm md:grid-cols-[max-content_minmax(0,1fr)]">
            <dt className="text-muted-foreground">
              {t("pages.network.certificate.source")}
            </dt>
            <dd>
              {cert.self_signed
                ? t("pages.network.certificate.source_self_signed")
                : t("pages.network.certificate.source_file")}
            </dd>
            <dt className="text-muted-foreground">
              {t("pages.network.certificate.subject")}
            </dt>
            <dd className="break-all">{cert.subject}</dd>
            <dt className="text-muted-foreground">
              {t("pages.network.certificate.names")}
            </dt>
            <dd className="break-all">{cert.names.join(", ") || "—"}</dd>
            <dt className="text-muted-foreground">
              {t("pages.network.certificate.expires")}
            </dt>
            <dd>{formatExpiry(cert.not_after)}</dd>
            <dt className="text-muted-foreground">
              {t("pages.network.certificate.fingerprint")}
            </dt>
            <dd
              className="font-mono text-xs break-all select-all"
              data-testid="cert-fingerprint"
            >
              {cert.fingerprint}
            </dd>
          </dl>
        ) : (
          <p className="text-muted-foreground text-sm" data-testid="cert-none">
            {cert?.error
              ? t("pages.network.certificate.unreadable", {
                  message: cert.error,
                })
              : t("pages.network.certificate.none")}
          </p>
        )}
      </div>

      <div className="flex flex-col gap-4 py-4 md:grid md:grid-cols-[minmax(0,1fr)_minmax(240px,320px)] md:gap-6">
        <div className="max-w-full space-y-1 md:max-w-[clamp(18rem,42vw,28rem)]">
          <p className="text-sm font-medium">
            {t("pages.network.certificate.choice")}
          </p>
          <p className="text-muted-foreground text-xs leading-normal">
            {t("pages.network.certificate.choice_hint")}
          </p>
        </div>
        <RadioGroup
          value={source}
          onValueChange={(v) => setSource(v as CertSource)}
          className="gap-2"
          data-testid="cert-source"
        >
          <div className="flex items-center gap-2">
            <RadioGroupItem value="self-signed" id="cert-source-self-signed" />
            <Label htmlFor="cert-source-self-signed" className="font-normal">
              {t("pages.network.certificate.self_signed")}
            </Label>
          </div>
          <div className="flex items-center gap-2">
            <RadioGroupItem value="file" id="cert-source-file" />
            <Label htmlFor="cert-source-file" className="font-normal">
              {t("pages.network.certificate.external")}
            </Label>
          </div>
        </RadioGroup>
      </div>

      {source === "file" ? (
        <>
          <Field
            label={t("pages.network.certificate.cert_file")}
            hint={t("pages.network.certificate.cert_file_hint")}
            layout="setting-row"
          >
            <Input
              data-testid="cert-file"
              value={certFile}
              placeholder="/etc/letsencrypt/live/claw.example.com/fullchain.pem"
              onChange={(e) => setCertFile(e.target.value)}
            />
          </Field>
          <Field
            label={t("pages.network.certificate.key_file")}
            hint={t("pages.network.certificate.key_file_hint")}
            layout="setting-row"
          >
            <Input
              data-testid="cert-key-file"
              value={keyFile}
              placeholder="/etc/letsencrypt/live/claw.example.com/privkey.pem"
              onChange={(e) => setKeyFile(e.target.value)}
            />
          </Field>
          <Field
            label={t("pages.network.certificate.save")}
            hint={t("pages.network.certificate.save_hint")}
            layout="setting-row"
          >
            <Button
              variant="outline"
              onClick={() => void saveFiles()}
              disabled={busy || !certFile.trim() || !keyFile.trim()}
              data-testid="cert-save"
            >
              {t("pages.network.certificate.save")}
            </Button>
          </Field>
        </>
      ) : (
        <>
          {tls?.source === "file" && (
            <Field
              label={t("pages.network.certificate.use_self_signed")}
              hint={t("pages.network.certificate.use_self_signed_hint")}
              layout="setting-row"
            >
              <Button
                variant="outline"
                onClick={() => void useSelfSigned()}
                disabled={busy}
                data-testid="cert-use-self-signed"
              >
                {t("pages.network.certificate.use_self_signed")}
              </Button>
            </Field>
          )}
          <Field
            label={t("pages.network.certificate.regenerate")}
            hint={t("pages.network.certificate.regenerate_hint")}
            layout="setting-row"
          >
            <Button
              variant="outline"
              onClick={() => void regenerate()}
              disabled={busy}
              data-testid="cert-regenerate"
            >
              {t("pages.network.certificate.regenerate")}
            </Button>
          </Field>
        </>
      )}

      {(error || notice) && (
        <div className="py-3">
          {error && (
            <p
              className="bg-destructive/10 text-destructive rounded-md px-3 py-2 text-sm"
              role="alert"
              data-testid="cert-error"
            >
              {error}
            </p>
          )}
          {notice && !error && (
            <p className="text-sm text-emerald-600" data-testid="cert-notice">
              {notice}
            </p>
          )}
        </div>
      )}
    </ConfigSectionCard>
  )
}
