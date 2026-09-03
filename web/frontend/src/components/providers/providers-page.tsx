import { IconLoader2, IconPlus } from "@tabler/icons-react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { useTranslation } from "react-i18next"

import { type ProviderInfo, getProviders } from "@/api/providers"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"

import { AddProviderSheet } from "./add-provider-sheet"
import { DeleteProviderDialog } from "./delete-provider-dialog"
import { EditProviderSheet } from "./edit-provider-sheet"
import { ProviderCard } from "./provider-card"

export function ProvidersPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [editing, setEditing] = useState<ProviderInfo | null>(null)
  const [deleting, setDeleting] = useState<ProviderInfo | null>(null)
  const [addOpen, setAddOpen] = useState(false)

  const {
    data: providers = [],
    isPending: loading,
    error,
  } = useQuery({
    queryKey: ["providers"],
    queryFn: async () => {
      const data = await getProviders()
      // Display alphabetically by name; each entry keeps its backend index so
      // edit/delete still target the correct config slot.
      return [...data.providers].sort((a, b) => a.name.localeCompare(b.name))
    },
  })
  const fetchError = error
    ? error instanceof Error
      ? error.message
      : t("providers.loadError")
    : ""

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["providers"] })
  }

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.providers")}>
        <div className="flex items-center gap-3">
          <Button size="sm" variant="outline" onClick={() => setAddOpen(true)}>
            <IconPlus className="size-4" />
            {t("providers.add.button")}
          </Button>
        </div>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 sm:px-6">
        <div className="pt-2">
          <p className="text-muted-foreground mt-1 text-sm">
            {t("providers.description")}
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
          <div className="py-6">
            {providers.length === 0 ? (
              <p className="text-muted-foreground text-sm">
                {t("providers.empty")}
              </p>
            ) : (
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
                {providers.map((provider) => (
                  <ProviderCard
                    key={provider.index}
                    provider={provider}
                    onEdit={setEditing}
                    onDelete={setDeleting}
                  />
                ))}
              </div>
            )}
          </div>
        )}
      </div>

      <EditProviderSheet
        provider={editing}
        open={editing !== null}
        onClose={() => setEditing(null)}
        onSaved={refresh}
      />

      <AddProviderSheet
        open={addOpen}
        onClose={() => setAddOpen(false)}
        onSaved={refresh}
        existingNames={providers.map((p) => p.name)}
      />

      <DeleteProviderDialog
        provider={deleting}
        onClose={() => setDeleting(null)}
        onDeleted={refresh}
      />
    </div>
  )
}
