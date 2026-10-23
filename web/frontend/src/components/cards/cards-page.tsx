import {
  IconDownload,
  IconLoader2,
  IconPlus,
  IconTrash,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { type ChangeEvent, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  CARDS_QUERY_KEY,
  type CardSummary,
  CardsAPIError,
  cardDownloadUrl,
  defaultCardId,
  deleteCard,
  getCards,
  importCard,
} from "@/api/cards"
import { PageHeader } from "@/components/page-header"
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
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

// cardLabel names a card as the page shows it: the name, with the nickname
// in parentheses when set.
export function cardLabel(c: { name: string; nickname?: string }): string {
  return c.nickname ? `${c.name} (${c.nickname})` : c.name
}

function formatModified(unixSeconds: number): string {
  if (!unixSeconds) return "—"
  return new Date(unixSeconds * 1000).toLocaleString()
}

// CardsPage lists the character-card library with Import, Download and
// Delete. There is no in-browser editor: the ccv3 tool and the file are the
// editing paths.
export function CardsPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const fileInputRef = useRef<HTMLInputElement | null>(null)
  const [importOpen, setImportOpen] = useState(false)
  const [importFile, setImportFile] = useState<File | null>(null)
  const [importId, setImportId] = useState("")
  const [replace, setReplace] = useState(false)
  const [pendingDelete, setPendingDelete] = useState<CardSummary | null>(null)

  const { data, isLoading, error } = useQuery({
    queryKey: CARDS_QUERY_KEY,
    queryFn: getCards,
  })

  const resetImport = () => {
    setImportOpen(false)
    setImportFile(null)
    setImportId("")
    setReplace(false)
  }

  const importMutation = useMutation({
    mutationFn: async () => importCard(importFile!, importId.trim(), replace),
    onSuccess: (res) => {
      toast.success(t("cards.importSuccess", { id: res.id }))
      resetImport()
      void queryClient.invalidateQueries({ queryKey: CARDS_QUERY_KEY })
    },
    onError: (err) => {
      if (err instanceof CardsAPIError && err.status === 409) {
        toast.error(t("cards.importExists"))
        return
      }
      toast.error(err instanceof Error ? err.message : String(err))
    },
  })

  const deleteMutation = useMutation({
    mutationFn: async (id: string) => deleteCard(id),
    onSuccess: () => {
      toast.success(t("cards.deleteSuccess"))
      setPendingDelete(null)
      void queryClient.invalidateQueries({ queryKey: CARDS_QUERY_KEY })
    },
    onError: (err) => {
      // A 409 is the server's own sentence naming the agents using the card.
      toast.error(err instanceof Error ? err.message : String(err))
      setPendingDelete(null)
    },
  })

  const handleFileChange = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0] ?? null
    setImportFile(file)
    if (file) setImportId(defaultCardId(file.name))
  }

  const cards = data?.cards ?? []
  const invalid = data?.invalid ?? []

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.cards")}>
        <Button
          variant="outline"
          onClick={() => setImportOpen(true)}
          disabled={importMutation.isPending}
        >
          <IconPlus className="size-4" />
          {t("cards.import")}
        </Button>
      </PageHeader>

      <div className="flex-1 overflow-auto px-6 py-3">
        <div className="w-full max-w-6xl space-y-6">
          {isLoading ? (
            <div className="text-muted-foreground py-6 text-sm">
              {t("labels.loading")}
            </div>
          ) : error ? (
            <div className="text-destructive py-6 text-sm">
              {t("cards.loadError")}
            </div>
          ) : (
            <section className="space-y-5">
              <p className="text-muted-foreground text-sm">{t("cards.hint")}</p>

              {cards.length > 0 ? (
                <div className="border-border/60 overflow-x-auto rounded-xl border">
                  <table className="w-full text-sm">
                    <thead className="text-muted-foreground text-left text-[11px] tracking-[0.18em] uppercase">
                      <tr>
                        <th className="px-3 py-2 font-medium">
                          {t("cards.name")}
                        </th>
                        <th className="px-3 py-2 font-medium">
                          {t("cards.id")}
                        </th>
                        <th className="px-3 py-2 font-medium">
                          {t("cards.tags")}
                        </th>
                        <th className="px-3 py-2 font-medium">
                          {t("cards.modified")}
                        </th>
                        <th className="px-3 py-2 font-medium">
                          {t("cards.usedBy")}
                        </th>
                        <th className="px-3 py-2">
                          <span className="sr-only">{t("cards.actions")}</span>
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {cards.map((c) => (
                        <tr
                          key={c.id}
                          className="border-border/60 border-t align-top"
                          data-testid="card-row"
                        >
                          <td className="px-3 py-2 font-medium">
                            {cardLabel(c)}
                          </td>
                          <td className="px-3 py-2 font-mono text-xs">
                            {c.id}
                          </td>
                          <td className="px-3 py-2">
                            {c.tags.length > 0 ? (
                              <div className="flex flex-wrap gap-1">
                                {c.tags.map((tag) => (
                                  <span
                                    key={tag}
                                    className="bg-muted rounded-md px-1.5 py-0.5 text-xs"
                                  >
                                    {tag}
                                  </span>
                                ))}
                              </div>
                            ) : (
                              <span className="text-muted-foreground">—</span>
                            )}
                          </td>
                          <td className="text-muted-foreground px-3 py-2 text-xs whitespace-nowrap">
                            {formatModified(c.modification_date)}
                          </td>
                          <td className="px-3 py-2">
                            {c.attached_to.length > 0 ? (
                              c.attached_to.join(", ")
                            ) : (
                              <span className="text-muted-foreground">—</span>
                            )}
                          </td>
                          <td className="px-3 py-2">
                            <div className="flex items-center justify-end gap-1">
                              <Button
                                asChild
                                variant="ghost"
                                size="icon-sm"
                                className="text-muted-foreground hover:text-foreground"
                              >
                                <a
                                  href={cardDownloadUrl(c.id)}
                                  download={`${c.id}.json`}
                                  title={t("cards.download")}
                                  aria-label={t("cards.download")}
                                >
                                  <IconDownload className="size-4" />
                                </a>
                              </Button>
                              <Button
                                variant="ghost"
                                size="icon-sm"
                                className="text-muted-foreground hover:text-destructive"
                                onClick={() => setPendingDelete(c)}
                                title={t("cards.delete")}
                                aria-label={t("cards.delete")}
                              >
                                <IconTrash className="size-4" />
                              </Button>
                            </div>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              ) : (
                <Card className="border-dashed">
                  <CardContent className="text-muted-foreground py-10 text-center text-sm">
                    {t("cards.empty")}
                  </CardContent>
                </Card>
              )}

              {invalid.length > 0 && (
                <div className="space-y-1">
                  <p className="text-muted-foreground text-xs font-semibold">
                    {t("cards.invalidTitle")}
                  </p>
                  {invalid.map((inv) => (
                    <p
                      key={inv.file}
                      data-testid="invalid-card"
                      className="text-xs text-amber-600 dark:text-amber-400"
                    >
                      {t("cards.invalidFile", {
                        file: inv.file,
                        reason: inv.reason,
                      })}
                    </p>
                  ))}
                </div>
              )}
            </section>
          )}
        </div>
      </div>

      <AlertDialog
        open={importOpen}
        onOpenChange={(open) => {
          if (!open && !importMutation.isPending) resetImport()
        }}
      >
        <AlertDialogContent size="sm">
          <AlertDialogHeader>
            <AlertDialogTitle>{t("cards.importTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("cards.importDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="space-y-3">
            <div className="space-y-1.5">
              <Label htmlFor="card-import-file">{t("cards.file")}</Label>
              <input
                ref={fileInputRef}
                id="card-import-file"
                type="file"
                accept=".json,.png,application/json,image/png"
                className="hidden"
                onChange={handleFileChange}
              />
              <div className="flex items-center gap-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => fileInputRef.current?.click()}
                >
                  {t("cards.chooseFile")}
                </Button>
                <span className="text-muted-foreground truncate text-xs">
                  {importFile?.name ?? ""}
                </span>
              </div>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="card-import-id">{t("cards.id")}</Label>
              <Input
                id="card-import-id"
                value={importId}
                onChange={(e) => setImportId(e.target.value)}
                placeholder={t("cards.idPlaceholder")}
                className="h-8 font-mono text-xs"
              />
            </div>
            <label className="flex items-center gap-2 text-sm select-none">
              <Checkbox
                checked={replace}
                onCheckedChange={(c) => setReplace(c === true)}
                aria-label={t("cards.replace")}
              />
              {t("cards.replace")}
            </label>
          </div>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={importMutation.isPending}>
              {t("common.cancel")}
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={
                importMutation.isPending ||
                importFile === null ||
                importId.trim() === ""
              }
              onClick={(e) => {
                // Keep the dialog open until the import succeeds.
                e.preventDefault()
                importMutation.mutate()
              }}
            >
              {importMutation.isPending ? (
                <IconLoader2 className="size-4 animate-spin" />
              ) : (
                <IconPlus className="size-4" />
              )}
              {t("cards.importConfirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null)
        }}
      >
        <AlertDialogContent size="sm">
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("cards.deleteTitle", {
                name: pendingDelete ? cardLabel(pendingDelete) : "",
              })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("cards.deleteDescription", { id: pendingDelete?.id })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteMutation.isPending}>
              {t("common.cancel")}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={deleteMutation.isPending || !pendingDelete}
              onClick={(e) => {
                e.preventDefault()
                if (pendingDelete) deleteMutation.mutate(pendingDelete.id)
              }}
            >
              {deleteMutation.isPending ? (
                <IconLoader2 className="size-4 animate-spin" />
              ) : (
                <IconTrash className="size-4" />
              )}
              {t("cards.deleteConfirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
