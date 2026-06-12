import { IconLoader2 } from "@tabler/icons-react"
import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import { type ProviderInfo, deleteProvider } from "@/api/providers"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"

interface DeleteProviderDialogProps {
  provider: ProviderInfo | null
  onClose: () => void
  onDeleted: () => void
}

export function DeleteProviderDialog({
  provider,
  onClose,
  onDeleted,
}: DeleteProviderDialogProps) {
  const { t } = useTranslation()
  const [deleting, setDeleting] = useState(false)
  const [error, setError] = useState("")

  useEffect(() => {
    if (provider) setError("")
  }, [provider])

  const handleConfirm = async () => {
    if (!provider) return
    setDeleting(true)
    setError("")
    try {
      await deleteProvider(provider.index)
      onDeleted()
      onClose()
    } catch (e) {
      // A 409 here means models still reference the provider — surface it.
      setError(e instanceof Error ? e.message : t("providers.delete.error"))
    } finally {
      setDeleting(false)
    }
  }

  return (
    <AlertDialog
      open={provider !== null}
      onOpenChange={(v) => !v && onClose()}
    >
      <AlertDialogContent size="sm">
        <AlertDialogHeader>
          <AlertDialogTitle>{t("providers.delete.title")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("providers.delete.description", { name: provider?.name })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <p className="text-destructive bg-destructive/10 rounded-md px-3 py-2 text-sm">
            {error}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel onClick={onClose} disabled={deleting}>
            {t("common.cancel")}
          </AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            onClick={handleConfirm}
            disabled={deleting}
          >
            {deleting && <IconLoader2 className="size-4 animate-spin" />}
            {t("providers.delete.confirm")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
