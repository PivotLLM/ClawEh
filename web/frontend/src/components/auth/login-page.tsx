import { IconLoader2, IconLock, IconRefresh } from "@tabler/icons-react"
import { useQuery } from "@tanstack/react-query"
import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import { LoginError, getAuthStatus, login } from "@/api/auth"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { safeNext } from "@/lib/auth-redirect"

interface LoginPageProps {
  /** Where to go after signing in; validated by safeNext. */
  next?: string
}

// LoginPage is the only thing an unauthenticated visitor sees. Three states:
// loading the auth status, "no admin account" (the server has never had
// `claw admin` run — nothing the browser can do about it), and the form.
export function LoginPage({ next }: LoginPageProps) {
  const { t } = useTranslation()
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const target = safeNext(next)

  const statusQuery = useQuery({
    queryKey: ["auth-status"],
    queryFn: getAuthStatus,
    retry: false,
  })
  const status = statusQuery.data ?? null
  const statusError = statusQuery.isError

  // Already signed in (a stale tab, a typed URL): straight back into the app.
  useEffect(() => {
    if (status?.authenticated) {
      window.location.assign(target)
    }
  }, [status, target])

  const retry = () => void statusQuery.refetch()

  const onSubmit = async (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    setSubmitting(true)
    setError(null)
    try {
      await login(username, password)
      // A full navigation, not a router push: the app re-reads the auth status
      // and starts the chat connection from a clean state.
      window.location.assign(target)
    } catch (err) {
      if (err instanceof LoginError && err.status === 401) {
        setError(t("auth.login.invalid"))
      } else if (err instanceof LoginError && err.status === 429) {
        setError(t("auth.login.locked", { seconds: err.retryAfter ?? 60 }))
      } else {
        setError(
          t("auth.login.failed", {
            message: err instanceof Error ? err.message : String(err),
          }),
        )
      }
      setPassword("")
      setSubmitting(false)
    }
  }

  return (
    <div className="flex min-h-dvh items-center justify-center px-4">
      <div className="border-border/60 bg-card w-full max-w-sm rounded-xl border p-8 shadow-sm">
        <div className="mb-6 flex items-center gap-3">
          <img className="h-10 w-auto" src="/logo.png" alt="" />
          <div>
            <h1 className="text-lg font-semibold">{t("auth.login.title")}</h1>
            <p className="text-muted-foreground text-sm">
              {t("auth.login.subtitle")}
            </p>
          </div>
        </div>

        {statusQuery.isPending && (
          <div
            className="text-muted-foreground flex items-center gap-2 text-sm"
            data-testid="login-loading"
          >
            <IconLoader2 className="size-4 animate-spin" />
            {t("auth.login.checking")}
          </div>
        )}

        {statusError && (
          <div className="space-y-4" data-testid="login-unreachable">
            <p className="text-destructive text-sm">
              {t("auth.login.unreachable")}
            </p>
            <Button variant="outline" onClick={retry}>
              <IconRefresh className="size-4" />
              {t("auth.noAdmin.retry")}
            </Button>
          </div>
        )}

        {status !== null && !status.configured && (
          <div className="space-y-4" data-testid="login-no-admin">
            <div className="flex items-start gap-2">
              <IconLock className="text-muted-foreground mt-0.5 size-4 shrink-0" />
              <div className="space-y-2 text-sm">
                <p className="font-medium">{t("auth.noAdmin.title")}</p>
                <p className="text-muted-foreground">
                  {t("auth.noAdmin.body")}
                </p>
                <pre className="bg-muted rounded-md px-3 py-2 font-mono text-sm">
                  claw admin
                </pre>
                <p className="text-muted-foreground">
                  {t("auth.noAdmin.hint")}
                </p>
              </div>
            </div>
            <Button variant="outline" onClick={retry}>
              <IconRefresh className="size-4" />
              {t("auth.noAdmin.retry")}
            </Button>
          </div>
        )}

        {status !== null && status.configured && (
          <form
            className="space-y-4"
            onSubmit={(e) => void onSubmit(e)}
            data-testid="login-form"
          >
            <div className="space-y-2">
              <Label htmlFor="login-username">{t("auth.login.username")}</Label>
              <Input
                id="login-username"
                name="username"
                autoComplete="username"
                required
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                disabled={submitting}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="login-password">{t("auth.login.password")}</Label>
              <Input
                id="login-password"
                name="password"
                type="password"
                autoComplete="current-password"
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                disabled={submitting}
              />
            </div>
            {error && (
              <p
                className="text-destructive text-sm"
                role="alert"
                data-testid="login-error"
              >
                {error}
              </p>
            )}
            <Button type="submit" className="w-full" disabled={submitting}>
              {submitting ? (
                <>
                  <IconLoader2 className="size-4 animate-spin" />
                  {t("auth.login.submitting")}
                </>
              ) : (
                t("auth.login.submit")
              )}
            </Button>
          </form>
        )}
      </div>
    </div>
  )
}
