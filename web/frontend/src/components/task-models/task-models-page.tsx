import { IconArrowDown, IconArrowUp, IconTrash } from "@tabler/icons-react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { useEffect, useRef, useState } from "react"
import { toast } from "sonner"

import {
  TASK_USES,
  type TaskModelEntry,
  type TaskModelLists,
  type TaskModelOption,
  type TaskModelsResponse,
  type TaskUse,
  emptyLists,
  getTaskModels,
  saveTaskModels,
} from "@/api/task-models"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"

import {
  EMPTY_NOTES,
  LIST_DESCRIPTIONS,
  LIST_TITLES,
  addModel,
  availableModels,
  moveRow,
  parseResultChars,
  removeRow,
  taskModelsPayload,
  unsupported,
} from "./task-models-model"

export const TASK_MODELS_QUERY_KEY = ["task-models"]

type SaveStatus = "saving" | "saved" | "error" | null

interface PageState {
  lists: TaskModelLists
  resultChars: string
  echo: boolean
}

function stateOf(data: TaskModelsResponse | undefined): PageState {
  const tm = data?.task_models
  const lists = emptyLists()
  for (const use of TASK_USES) lists[use] = tm?.[use] ?? []
  return {
    lists,
    resultChars: tm?.result_text_chars ? String(tm.result_text_chars) : "",
    echo: tm?.echo_transcription === true,
  }
}

const warnClass = "text-xs text-amber-600 dark:text-amber-400"
const selectClass =
  "border-input bg-background h-9 rounded-md border px-3 text-sm"

export function TaskModelsPage() {
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: TASK_MODELS_QUERY_KEY,
    queryFn: getTaskModels,
  })

  // Editable copy of the fetched block, reseeded whenever a fresh fetch
  // lands, and seeded from the cache on the first render so a revisit never
  // shows (and then saves) an empty page.
  const [state, setState] = useState<PageState>(() => stateOf(query.data))
  const [syncedData, setSyncedData] = useState(query.data)
  if (query.data && query.data !== syncedData) {
    setSyncedData(query.data)
    setState(stateOf(query.data))
  }

  const options = query.data?.models ?? []
  const defaultChars = query.data?.defaults.result_text_chars ?? 0
  const charsValue = parseResultChars(state.resultChars)

  const [status, setStatus] = useState<SaveStatus>(null)
  const stateRef = useRef(state)
  useEffect(() => {
    stateRef.current = state
  }, [state])
  const saveTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const savedTimer = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined,
  )
  // pendingSave: an edit is waiting for its debounced save. Leaving the
  // page saves it at once rather than dropping it.
  const pendingSave = useRef(false)

  // doSave writes the whole block. It does not refetch the page, so a field
  // being typed is not replaced mid-edit; the Agents page, whose classifier
  // models follow the Classification list, is refreshed.
  const doSave = async () => {
    pendingSave.current = false
    const s = stateRef.current
    const chars = parseResultChars(s.resultChars)
    if (chars === null) return // the field shows why
    setStatus("saving")
    try {
      await saveTaskModels(taskModelsPayload(s.lists, chars, s.echo))
      setStatus("saved")
      clearTimeout(savedTimer.current)
      savedTimer.current = setTimeout(() => setStatus(null), 2000)
      void queryClient.invalidateQueries({ queryKey: ["agents-page"] })
    } catch (e) {
      setStatus("error")
      toast.error(e instanceof Error ? e.message : "Save failed")
    }
  }
  const doSaveRef = useRef(doSave)
  useEffect(() => {
    doSaveRef.current = doSave
  })
  const scheduleSave = () => {
    clearTimeout(saveTimer.current)
    pendingSave.current = true
    saveTimer.current = setTimeout(() => void doSaveRef.current(), 600)
  }
  useEffect(
    () => () => {
      clearTimeout(saveTimer.current)
      clearTimeout(savedTimer.current)
      if (pendingSave.current) {
        pendingSave.current = false
        void doSaveRef.current()
      }
    },
    [],
  )

  const change = (fn: (s: PageState) => PageState) => {
    setState(fn)
    scheduleSave()
  }
  const changeList = (
    use: TaskUse,
    fn: (list: TaskModelEntry[]) => TaskModelEntry[],
  ) => change((s) => ({ ...s, lists: { ...s.lists, [use]: fn(s.lists[use]) } }))

  return (
    <div className="flex h-full flex-col">
      <PageHeader title="Task models">
        {status && (
          <span
            className={`text-xs ${status === "error" ? "text-destructive" : status === "saved" ? "text-emerald-500" : "text-muted-foreground"}`}
          >
            {status === "saving"
              ? "Saving…"
              : status === "saved"
                ? "Saved ✓"
                : "Save failed"}
          </span>
        )}
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 sm:px-6">
        <div className="flex flex-col gap-6 pt-2 pb-6">
          <p className="text-muted-foreground text-sm">
            Models used for single calls without tools. Each list is tried from
            the top down. Add models on the{" "}
            <Link to="/models" className="underline">
              Models
            </Link>{" "}
            page first.
          </p>

          {query.error && (
            <p className="text-destructive text-sm">
              {query.error instanceof Error
                ? query.error.message
                : "Could not load task models."}
            </p>
          )}

          {/* Nothing is editable until the saved block is loaded: an edit made
          before would save over it. */}
          {query.data && (
            <>
              {TASK_USES.map((use) => (
                <ListSection
                  key={use}
                  use={use}
                  list={state.lists[use]}
                  options={options}
                  onChange={(fn) => changeList(use, fn)}
                />
              ))}

              <Card>
                <CardHeader>
                  <CardTitle>Settings</CardTitle>
                </CardHeader>
                <CardContent className="flex flex-col gap-4">
                  <div className="flex max-w-sm flex-col gap-1.5">
                    <Label htmlFor="task-result-chars">
                      Text in results (characters)
                    </Label>
                    <Input
                      id="task-result-chars"
                      inputMode="numeric"
                      value={state.resultChars}
                      placeholder={String(defaultChars)}
                      onChange={(e) =>
                        change((s) => ({ ...s, resultChars: e.target.value }))
                      }
                    />
                    {charsValue === null ? (
                      <p className="text-destructive text-xs">
                        Enter a whole number, or leave it blank.
                      </p>
                    ) : (
                      <p className="text-muted-foreground text-xs">
                        Default {defaultChars}.
                      </p>
                    )}
                  </div>
                  <div className="flex items-center justify-between gap-3">
                    <div className="flex flex-col gap-1">
                      <Label htmlFor="task-echo">Echo transcription</Label>
                      <p className="text-muted-foreground text-xs">
                        Sends the transcript of a voice message back to the
                        sender.
                      </p>
                    </div>
                    <Switch
                      id="task-echo"
                      checked={state.echo}
                      onCheckedChange={(v) =>
                        change((s) => ({ ...s, echo: v }))
                      }
                    />
                  </div>
                  {state.echo && state.lists.stt.length === 0 && (
                    <p data-testid="task-echo-no-model" className={warnClass}>
                      No model in Transcription, so nothing is transcribed.
                    </p>
                  )}
                </CardContent>
              </Card>
            </>
          )}
        </div>
      </div>
    </div>
  )
}

interface ListSectionProps {
  use: TaskUse
  list: TaskModelEntry[]
  options: TaskModelOption[]
  onChange: (fn: (list: TaskModelEntry[]) => TaskModelEntry[]) => void
}

/** ListSection is one use's list: its rows in fallback order, each with
 *  Move up / Move down / Remove and the list's own settings, and the list's
 *  own Model to add. Accessible names carry the list's title so they stay
 *  unique on the page. */
function ListSection({ use, list, options, onChange }: ListSectionProps) {
  const title = LIST_TITLES[use]
  const addable = availableModels(list, options)
  const [toAdd, setToAdd] = useState("")
  const addChoice = addable.includes(toAdd) ? toAdd : (addable[0] ?? "")
  const changeEntry = (i: number, fn: (e: TaskModelEntry) => TaskModelEntry) =>
    onChange((l) => l.map((e, idx) => (idx === i ? fn(e) : e)))

  return (
    <Card data-testid={`task-list-${use}`}>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
        <CardDescription>{LIST_DESCRIPTIONS[use]}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {list.length === 0 && (
          <p
            data-testid={`task-list-empty-${use}`}
            className="text-muted-foreground text-sm"
          >
            {EMPTY_NOTES[use]}
          </p>
        )}
        {list.map((entry, i) => {
          const option = options.find((o) => o.name === entry.model)
          const idBase = `task-${use}-${i}`
          return (
            <div
              key={entry.model}
              data-testid={`task-model-${use}-${entry.model}`}
              className="flex flex-col gap-3 rounded-md border p-4"
            >
              <div className="flex items-center justify-between gap-3">
                <span className="text-sm font-medium">{entry.model}</span>
                <div className="flex items-center gap-1">
                  <Button
                    variant="ghost"
                    size="icon"
                    aria-label={`Move ${entry.model} up in ${title}`}
                    disabled={i === 0}
                    onClick={() => onChange((l) => moveRow(l, i, -1))}
                  >
                    <IconArrowUp className="size-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    aria-label={`Move ${entry.model} down in ${title}`}
                    disabled={i === list.length - 1}
                    onClick={() => onChange((l) => moveRow(l, i, 1))}
                  >
                    <IconArrowDown className="size-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    aria-label={`Remove ${entry.model} from ${title}`}
                    onClick={() => onChange((l) => removeRow(l, i))}
                  >
                    <IconTrash className="size-4" />
                  </Button>
                </div>
              </div>

              {!option && (
                <p className={warnClass}>
                  Not on the Models page, so it is not used.
                </p>
              )}
              {option && !option.enabled && (
                <p className={warnClass}>
                  Disabled on the{" "}
                  <Link to="/models" className="underline">
                    Models
                  </Link>{" "}
                  page, so it is not used.
                </p>
              )}
              {unsupported(use, option) && (
                <p
                  data-testid={`task-model-unsupported-${use}-${entry.model}`}
                  className={warnClass}
                >
                  {entry.model}&apos;s provider can&apos;t do this.
                </p>
              )}

              {use === "image" && (
                <div className="flex max-w-sm flex-col gap-1.5">
                  <Label htmlFor={`${idBase}-image-api`}>Images through</Label>
                  <select
                    id={`${idBase}-image-api`}
                    aria-label={`Images through for ${entry.model}`}
                    className={selectClass}
                    value={entry.image_api === "images" ? "images" : "chat"}
                    onChange={(e) =>
                      changeEntry(i, (r) => ({
                        ...r,
                        image_api:
                          e.target.value === "images" ? "images" : undefined,
                      }))
                    }
                  >
                    <option value="chat">Chat (image output)</option>
                    <option value="images">Images API</option>
                  </select>
                </div>
              )}

              {use === "tts" && (
                <div className="grid gap-3 sm:grid-cols-2">
                  <div className="flex flex-col gap-1.5">
                    <Label htmlFor={`${idBase}-voice`}>Voice</Label>
                    <Input
                      id={`${idBase}-voice`}
                      aria-label={`Voice for ${entry.model}`}
                      value={entry.tts_defaults?.voice ?? ""}
                      placeholder="The model's default"
                      onChange={(e) =>
                        changeEntry(i, (r) => ({
                          ...r,
                          tts_defaults: {
                            ...r.tts_defaults,
                            voice: e.target.value,
                          },
                        }))
                      }
                    />
                  </div>
                  <div className="flex flex-col gap-1.5">
                    <Label htmlFor={`${idBase}-format`}>Format</Label>
                    <Input
                      id={`${idBase}-format`}
                      aria-label={`Format for ${entry.model}`}
                      value={entry.tts_defaults?.format ?? ""}
                      placeholder="The model's default"
                      onChange={(e) =>
                        changeEntry(i, (r) => ({
                          ...r,
                          tts_defaults: {
                            ...r.tts_defaults,
                            format: e.target.value,
                          },
                        }))
                      }
                    />
                  </div>
                </div>
              )}
            </div>
          )
        })}

        <div className="flex flex-wrap items-center gap-3">
          {addable.length > 0 ? (
            <>
              <select
                aria-label={`Model to add to ${title}`}
                className={selectClass}
                value={addChoice}
                onChange={(e) => setToAdd(e.target.value)}
              >
                {addable.map((name) => (
                  <option key={name} value={name}>
                    {name}
                  </option>
                ))}
              </select>
              <Button
                variant="outline"
                aria-label={`Add model to ${title}`}
                onClick={() => onChange((l) => addModel(l, addChoice))}
              >
                Add model
              </Button>
            </>
          ) : (
            <span className="text-muted-foreground text-sm">
              Every enabled model is in this list. Add more on the{" "}
              <Link to="/models" className="underline">
                Models
              </Link>{" "}
              page.
            </span>
          )}
        </div>
      </CardContent>
    </Card>
  )
}
