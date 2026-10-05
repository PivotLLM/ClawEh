import { IconLoader2 } from "@tabler/icons-react"
import { useState } from "react"
import { useTranslation } from "react-i18next"

import { type ModelInfo, deleteModel } from "@/api/models"
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

interface DeleteModelDialogProps {
  model: ModelInfo | null
  onClose: () => void
  onDeleted: () => void
}

export function DeleteModelDialog({
  model,
  onClose,
  onDeleted,
}: DeleteModelDialogProps) {
  const { t } = useTranslation()
  const [deleting, setDeleting] = useState(false)
  const [error, setError] = useState("")

  // Clear a stale error when a different model is targeted. Adjusted during
  // render rather than in an effect so the previous model's error is never
  // shown against the new one, even for a frame.
  const [syncedModel, setSyncedModel] = useState(model)
  if (model && model !== syncedModel) {
    setSyncedModel(model)
    setError("")
  }

  const handleConfirm = async () => {
    if (!model) return
    if (model.is_default) {
      onClose()
      return
    }
    setDeleting(true)
    setError("")
    try {
      await deleteModel(model.index)
      onDeleted()
      onClose()
    } catch (e) {
      // A 409 here means an agent, default or summarization chain still
      // references the model — surface it so the operator knows what to repoint.
      setError(e instanceof Error ? e.message : t("models.delete.error"))
    } finally {
      setDeleting(false)
    }
  }

  return (
    <AlertDialog open={model !== null} onOpenChange={(v) => !v && onClose()}>
      <AlertDialogContent size="sm">
        <AlertDialogHeader>
          <AlertDialogTitle>{t("models.delete.title")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("models.delete.description", { name: model?.model_name })}
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
            {t("models.delete.confirm")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
