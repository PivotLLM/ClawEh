import { IconDownload, IconUpload } from "@tabler/icons-react"
import { useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  MEMORY_TYPES,
  MEMORY_TYPE_HINTS,
  type MemoryDomain,
  type MemoryType,
  memoryExportURL,
} from "@/api/memory"
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
import { Textarea } from "@/components/ui/textarea"

/** Bulk actions over the current selection.
 *
 *  This is on the critical path rather than a convenience: a scheduled job
 *  writing one memory per run produces hundreds of near-identical rows, and
 *  retyping or clearing them one at a time is not a workflow anybody finishes.
 */
export function BulkBar({
  count,
  busy,
  onRetype,
  onRetire,
  onRestore,
  onDelete,
  onClear,
}: {
  count: number
  busy: boolean
  onRetype: (t: MemoryType) => void
  onRetire: () => void
  onRestore: () => void
  onDelete: () => void
  onClear: () => void
}) {
  const { t } = useTranslation()
  if (count === 0) return null
  return (
    <div
      data-testid="bulk-bar"
      className="bg-muted border-border sticky top-0 z-10 flex flex-wrap items-center gap-2 rounded-md border px-3 py-2 text-sm"
    >
      <span className="font-medium">
        {t("pages.memory.selected_count", { count })}
      </span>
      <Select
        disabled={busy}
        value=""
        onValueChange={(v) => onRetype(v as MemoryType)}
      >
        <SelectTrigger
          size="sm"
          className="h-8 w-[11rem]"
          aria-label={t("pages.memory.bulk_retype")}
        >
          <SelectValue placeholder={t("pages.memory.bulk_retype")} />
        </SelectTrigger>
        <SelectContent>
          {MEMORY_TYPES.map((ty) => (
            <SelectItem key={ty} value={ty}>
              <div className="flex flex-col gap-0.5">
                <span className="font-medium">{ty}</span>
                <span className="text-muted-foreground text-xs">
                  {MEMORY_TYPE_HINTS[ty]}
                </span>
              </div>
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Button size="sm" variant="outline" disabled={busy} onClick={onRetire}>
        {t("pages.memory.bulk_retire")}
      </Button>
      <Button size="sm" variant="outline" disabled={busy} onClick={onRestore}>
        {t("pages.memory.bulk_restore")}
      </Button>
      <Button
        size="sm"
        variant="destructive"
        disabled={busy}
        onClick={onDelete}
      >
        {t("pages.memory.bulk_delete")}
      </Button>
      <Button size="sm" variant="ghost" disabled={busy} onClick={onClear}>
        {t("pages.memory.clear_selection")}
      </Button>
    </div>
  )
}

/** Export downloads, import uploads. Export is a plain link so the browser does
 *  the download itself rather than the page buffering the whole document. */
export function TransferControls({
  storeId,
  busy,
  onImport,
}: {
  storeId: string
  busy: boolean
  onImport: (yaml: string, mode: "merge" | "replace") => void
}) {
  const { t } = useTranslation()
  const fileRef = useRef<HTMLInputElement>(null)
  const [mode, setMode] = useState<"merge" | "replace">("merge")

  const pick = async (file: File | undefined) => {
    if (!file) return
    onImport(await file.text(), mode)
  }

  return (
    <div className="flex flex-wrap items-center gap-2">
      <Button asChild size="sm" variant="outline">
        <a href={memoryExportURL(storeId)} download>
          <IconDownload className="size-4" />
          {t("pages.memory.export")}
        </a>
      </Button>

      <Select
        value={mode}
        onValueChange={(v) => setMode(v as "merge" | "replace")}
      >
        <SelectTrigger
          size="sm"
          className="h-8 w-[13rem]"
          aria-label={t("pages.memory.import_mode")}
        >
          <SelectValue>{t(`pages.memory.mode_${mode}`)}</SelectValue>
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="merge">
            <div className="flex flex-col gap-0.5">
              <span className="font-medium">
                {t("pages.memory.mode_merge")}
              </span>
              <span className="text-muted-foreground text-xs">
                {t("pages.memory.mode_merge_hint")}
              </span>
            </div>
          </SelectItem>
          <SelectItem value="replace">
            <div className="flex flex-col gap-0.5">
              <span className="font-medium">
                {t("pages.memory.mode_replace")}
              </span>
              <span className="text-muted-foreground text-xs">
                {t("pages.memory.mode_replace_hint")}
              </span>
            </div>
          </SelectItem>
        </SelectContent>
      </Select>

      <input
        ref={fileRef}
        type="file"
        accept=".yaml,.yml,text/yaml,application/yaml"
        className="hidden"
        onChange={(e) => {
          void pick(e.target.files?.[0])
          // Clear it, or picking the same file twice does nothing.
          e.target.value = ""
        }}
      />
      <Button
        size="sm"
        variant="outline"
        disabled={busy}
        onClick={() => fileRef.current?.click()}
      >
        <IconUpload className="size-4" />
        {t("pages.memory.import")}
      </Button>
    </div>
  )
}

/** Adds a memory by hand. What the operator writes is recorded with
 *  origin=user, which the assistant sees and is told outranks its own
 *  inferences — the one piece of provenance that is verifiable. */
export function AddMemoryForm({
  domain,
  busy,
  onSubmit,
  onCancel,
}: {
  domain: MemoryDomain
  busy: boolean
  onSubmit: (type: MemoryType, text: string) => void
  onCancel: () => void
}) {
  const { t } = useTranslation()
  const [type, setType] = useState<MemoryType>("fact")
  const [text, setText] = useState("")
  return (
    <form
      data-testid="add-memory-form"
      className="border-border space-y-2 rounded-lg border p-3"
      onSubmit={(e) => {
        e.preventDefault()
        if (text.trim() !== "") onSubmit(type, text.trim())
      }}
    >
      <div className="text-sm font-medium">
        {t("pages.memory.add_memory_to", { name: domain.name })}
      </div>
      <div className="flex flex-col gap-1">
        <Label htmlFor="new-memory-type">{t("pages.memory.type")}</Label>
        <Select value={type} onValueChange={(v) => setType(v as MemoryType)}>
          <SelectTrigger id="new-memory-type" className="w-full">
            <SelectValue>{type}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {MEMORY_TYPES.map((ty) => (
              <SelectItem key={ty} value={ty}>
                <div className="flex flex-col gap-0.5">
                  <span className="font-medium">{ty}</span>
                  <span className="text-muted-foreground text-xs">
                    {MEMORY_TYPE_HINTS[ty]}
                  </span>
                </div>
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div className="flex flex-col gap-1">
        <Label htmlFor="new-memory-text">{t("pages.memory.text")}</Label>
        <Textarea
          id="new-memory-text"
          value={text}
          rows={3}
          onChange={(e) => setText(e.target.value)}
          placeholder={t("pages.memory.text_placeholder")}
        />
      </div>
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={busy || text.trim() === ""}>
          {t("pages.memory.add")}
        </Button>
        <Button type="button" size="sm" variant="ghost" onClick={onCancel}>
          {t("labels.cancel")}
        </Button>
      </div>
    </form>
  )
}

/** Adds a domain, since a hand-written memory needs somewhere to live. */
export function AddDomainForm({
  busy,
  onSubmit,
  onCancel,
}: {
  busy: boolean
  onSubmit: (name: string, sticky: boolean) => void
  onCancel: () => void
}) {
  const { t } = useTranslation()
  const [name, setName] = useState("")
  const [sticky, setSticky] = useState(false)
  return (
    <form
      data-testid="add-domain-form"
      className="border-border space-y-2 rounded-lg border p-3"
      onSubmit={(e) => {
        e.preventDefault()
        if (name.trim() !== "") onSubmit(name.trim(), sticky)
      }}
    >
      <div className="text-sm font-medium">{t("pages.memory.add_domain")}</div>
      <div className="flex flex-col gap-1">
        <Label htmlFor="new-domain-name">{t("pages.memory.domain_name")}</Label>
        <Input
          id="new-domain-name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={t("pages.memory.domain_name_placeholder")}
        />
      </div>
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={sticky}
          onChange={(e) => setSticky(e.target.checked)}
        />
        {t("pages.memory.sticky_hint")}
      </label>
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={busy || name.trim() === ""}>
          {t("pages.memory.add")}
        </Button>
        <Button type="button" size="sm" variant="ghost" onClick={onCancel}>
          {t("labels.cancel")}
        </Button>
      </div>
    </form>
  )
}
