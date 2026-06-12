import { IconLoader2, IconPlus, IconStar } from "@tabler/icons-react"
import { useCallback, useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import { type ModelInfo, getModels, setDefaultModel, updateModel } from "@/api/models"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"

import { AddModelSheet } from "./add-model-sheet"
import { DeleteModelDialog } from "./delete-model-dialog"
import { EditModelSheet } from "./edit-model-sheet"
import { ProviderSection } from "./provider-section"

interface ProviderGroup {
  key: string
  label: string
  models: ModelInfo[]
  hasDefault: boolean
  configuredCount: number
}

export function ModelsPage() {
  const { t } = useTranslation()
  const [models, setModels] = useState<ModelInfo[]>([])
  const [loading, setLoading] = useState(true)
  const [fetchError, setFetchError] = useState("")

  const [editingModel, setEditingModel] = useState<ModelInfo | null>(null)
  const [deletingModel, setDeletingModel] = useState<ModelInfo | null>(null)
  const [addOpen, setAddOpen] = useState(false)
  const [settingDefaultIndex, setSettingDefaultIndex] = useState<number | null>(
    null,
  )

  const fetchModels = useCallback(async () => {
    try {
      const data = await getModels()
      // Sort by provider, then alphabetically by model name within each
      // provider. The provider groups are ordered alphabetically below; this
      // keeps each group's models in alphabetical order.
      const sorted = [...data.models].sort((a, b) => {
        const byProvider = (a.provider || "").localeCompare(b.provider || "")
        if (byProvider !== 0) return byProvider
        return a.model_name.localeCompare(b.model_name)
      })
      setModels(sorted)
      setFetchError("")
    } catch (e) {
      setFetchError(e instanceof Error ? e.message : t("models.loadError"))
    } finally {
      setLoading(false)
    }
  }, [t])

  useEffect(() => {
    fetchModels()
  }, [fetchModels])

  const handleToggleEnabled = async (model: ModelInfo) => {
    try {
      await updateModel(model.index, { ...model, enabled: !model.enabled })
      await fetchModels()
    } catch {
      // ignore
    }
  }

  const handleSetDefault = async (model: ModelInfo) => {
    if (model.is_default) return

    setSettingDefaultIndex(model.index)
    try {
      await setDefaultModel(model.model_name)
      await fetchModels()
    } catch {
      // ignore
    } finally {
      setSettingDefaultIndex(null)
    }
  }

  const grouped: Record<string, ModelInfo[]> = {}
  for (const model of models) {
    const providerKey = model.provider || t("models.noProvider")
    if (!grouped[providerKey]) {
      grouped[providerKey] = []
    }
    grouped[providerKey].push(model)
  }

  const providerGroups: ProviderGroup[] = Object.entries(grouped)
    .map(([key, groupModels]) => {
      const configuredCount = groupModels.filter(
        (model) => model.configured,
      ).length
      return {
        key,
        label: key,
        models: groupModels,
        hasDefault: groupModels.some((model) => model.is_default),
        configuredCount,
      }
    })
    .sort((a, b) => a.label.localeCompare(b.label))

  const defaultModel = models.find((model) => model.is_default)

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.models")}>
        <div className="flex items-center gap-3">
          <Button size="sm" variant="outline" onClick={() => setAddOpen(true)}>
            <IconPlus className="size-4" />
            {t("models.add.button")}
          </Button>
        </div>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 sm:px-6">
        <div className="pt-2">
          {!defaultModel && (
            <div className="text-muted-foreground flex items-center gap-1.5 text-sm">
              <span>{t("models.noDefaultHintPrefix")}</span>
              <IconStar className="size-3.5 shrink-0" />
              <span>{t("models.noDefaultHintSuffix")}</span>
            </div>
          )}
          <p className="text-muted-foreground mt-1 text-sm">
            {t("models.description")}
          </p>
        </div>

        {loading && (
          <div className="flex items-center justify-center py-20">
            <IconLoader2 className="text-muted-foreground size-6 animate-spin" />
          </div>
        )}

        {fetchError && (
          <div className="text-destructive bg-destructive/10 rounded-lg px-4 py-3 text-sm">
            {fetchError}
          </div>
        )}

        {!loading && !fetchError && (
          <div className="pb-8">
            {providerGroups.map((providerGroup) => (
              <ProviderSection
                key={providerGroup.key}
                provider={providerGroup.label}
                providerKey={providerGroup.key}
                models={providerGroup.models}
                onEdit={setEditingModel}
                onSetDefault={handleSetDefault}
                onDelete={setDeletingModel}
                onToggleEnabled={handleToggleEnabled}
                settingDefaultIndex={settingDefaultIndex}
              />
            ))}
          </div>
        )}
      </div>

      <EditModelSheet
        model={editingModel}
        open={editingModel !== null}
        onClose={() => setEditingModel(null)}
        onSaved={fetchModels}
      />

      <AddModelSheet
        open={addOpen}
        onClose={() => setAddOpen(false)}
        onSaved={fetchModels}
        existingModelNames={models.map((model) => model.model_name)}
      />

      <DeleteModelDialog
        model={deletingModel}
        onClose={() => setDeletingModel(null)}
        onDeleted={fetchModels}
      />
    </div>
  )
}
