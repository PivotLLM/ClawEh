import { IconArrowDown, IconArrowUp, IconTrash } from "@tabler/icons-react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { useEffect, useRef, useState } from "react"
import { toast } from "sonner"

import {
  TASK_USES,
  type TaskModel,
  type TaskModelsResponse,
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
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"

import {
  USE_LABELS,
  addModel,
  availableModels,
  modelsFor,
  moveRow,
  parseResultChars,
  setUse,
  taskModelsPayload,
  unsupportedUses,
} from "./task-models-model"

export const TASK_MODELS_QUERY_KEY = ["task-models"]

type SaveStatus = "saving" | "saved" | "error" | null

interface PageState {
  rows: TaskModel[]
  resultChars: string
  echo: boolean
}

function stateOf(data: TaskModelsResponse | undefined): PageState {
  const tm = data?.task_models
  return {
    rows: tm?.models ?? [],
    resultChars: tm?.result_text_chars ? String(tm.result_text_chars) : "",
    echo: tm?.echo_transcription === true,
  }
}

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
  // models follow the Classify ticks, is refreshed.
  const doSave = async () => {
    pendingSave.current = false
    const s = stateRef.current
    const chars = parseResultChars(s.resultChars)
    if (chars === null) return // the field shows why
    setStatus("saving")
    try {
      await saveTaskModels(taskModelsPayload(s.rows, chars, s.echo))
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
  const changeRow = (i: number, fn: (r: TaskModel) => TaskModel) =>
    change((s) => ({
      ...s,
      rows: s.rows.map((r, idx) => (idx === i ? fn(r) : r)),
    }))

  const addable = availableModels(state.rows, options)
  const [toAdd, setToAdd] = useState("")
  const addChoice = addable.includes(toAdd) ? toAdd : (addable[0] ?? "")

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
            Models used for single calls without tools. Each use tries its
            models from the top down.
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
              <Card>
                <CardHeader>
                  <CardTitle>Models</CardTitle>
                  <CardDescription>
                    Add models from the{" "}
                    <Link to="/models" className="underline">
                      Models
                    </Link>{" "}
                    page and tick what each is used for.
                  </CardDescription>
                </CardHeader>
                <CardContent className="flex flex-col gap-4">
                  {state.rows.length === 0 && (
                    <p className="text-muted-foreground text-sm">
                      No task models.
                    </p>
                  )}
                  {state.rows.map((row, i) => {
                    const option = options.find((o) => o.name === row.model)
                    const unsupported = unsupportedUses(row, option)
                    return (
                      <div
                        key={row.model}
                        data-testid={`task-model-${row.model}`}
                        className="flex flex-col gap-3 rounded-md border p-4"
                      >
                        <div className="flex items-center justify-between gap-3">
                          <span className="text-sm font-medium">
                            {row.model}
                          </span>
                          <div className="flex items-center gap-1">
                            <Button
                              variant="ghost"
                              size="icon"
                              aria-label={`Move ${row.model} up`}
                              disabled={i === 0}
                              onClick={() =>
                                change((s) => ({
                                  ...s,
                                  rows: moveRow(s.rows, i, -1),
                                }))
                              }
                            >
                              <IconArrowUp className="size-4" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              aria-label={`Move ${row.model} down`}
                              disabled={i === state.rows.length - 1}
                              onClick={() =>
                                change((s) => ({
                                  ...s,
                                  rows: moveRow(s.rows, i, 1),
                                }))
                              }
                            >
                              <IconArrowDown className="size-4" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              aria-label={`Remove ${row.model}`}
                              onClick={() =>
                                change((s) => ({
                                  ...s,
                                  rows: s.rows.filter((_, idx) => idx !== i),
                                }))
                              }
                            >
                              <IconTrash className="size-4" />
                            </Button>
                          </div>
                        </div>

                        <div className="flex flex-wrap gap-x-5 gap-y-2">
                          {TASK_USES.map((use) => {
                            const id = `task-${i}-${use}`
                            return (
                              <div
                                key={use}
                                className="flex items-center gap-2"
                              >
                                <Checkbox
                                  id={id}
                                  checked={row[use] === true}
                                  onCheckedChange={(v) =>
                                    changeRow(i, (r) =>
                                      setUse(r, use, v === true),
                                    )
                                  }
                                  aria-label={`${row.model}: ${USE_LABELS[use]}`}
                                />
                                <Label
                                  htmlFor={id}
                                  className="text-sm font-normal"
                                >
                                  {USE_LABELS[use]}
                                </Label>
                              </div>
                            )
                          })}
                        </div>

                        {!option && (
                          <p className="text-xs text-amber-600 dark:text-amber-400">
                            Not on the Models page, so it is not used.
                          </p>
                        )}
                        {option && !option.enabled && (
                          <p className="text-xs text-amber-600 dark:text-amber-400">
                            Disabled on the{" "}
                            <Link to="/models" className="underline">
                              Models
                            </Link>{" "}
                            page, so it is not used.
                          </p>
                        )}
                        {unsupported.length > 0 && (
                          <p
                            data-testid={`task-model-unsupported-${row.model}`}
                            className="text-xs text-amber-600 dark:text-amber-400"
                          >
                            {row.model}&apos;s provider can&apos;t do{" "}
                            {unsupported.map((u) => USE_LABELS[u]).join(", ")}.
                          </p>
                        )}

                        {row.image === true && (
                          <div className="flex max-w-sm flex-col gap-1.5">
                            <Label htmlFor={`task-${i}-image-api`}>
                              Images through
                            </Label>
                            <select
                              id={`task-${i}-image-api`}
                              className="border-input bg-background h-9 rounded-md border px-3 text-sm"
                              value={
                                row.image_api === "images" ? "images" : "chat"
                              }
                              onChange={(e) =>
                                changeRow(i, (r) => ({
                                  ...r,
                                  image_api:
                                    e.target.value === "images"
                                      ? "images"
                                      : undefined,
                                }))
                              }
                            >
                              <option value="chat">Chat (image output)</option>
                              <option value="images">Images API</option>
                            </select>
                          </div>
                        )}

                        {row.tts === true && (
                          <div className="grid gap-3 sm:grid-cols-2">
                            <div className="flex flex-col gap-1.5">
                              <Label htmlFor={`task-${i}-voice`}>Voice</Label>
                              <Input
                                id={`task-${i}-voice`}
                                value={row.tts_defaults?.voice ?? ""}
                                placeholder="The model's default"
                                onChange={(e) =>
                                  changeRow(i, (r) => ({
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
                              <Label htmlFor={`task-${i}-format`}>Format</Label>
                              <Input
                                id={`task-${i}-format`}
                                value={row.tts_defaults?.format ?? ""}
                                placeholder="The model's default"
                                onChange={(e) =>
                                  changeRow(i, (r) => ({
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
                          aria-label="Model to add"
                          className="border-input bg-background h-9 rounded-md border px-3 text-sm"
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
                          onClick={() =>
                            change((s) => ({
                              ...s,
                              rows: addModel(s.rows, addChoice),
                            }))
                          }
                        >
                          Add model
                        </Button>
                      </>
                    ) : (
                      <span className="text-muted-foreground text-sm">
                        Every model is listed. Add more on the{" "}
                        <Link to="/models" className="underline">
                          Models
                        </Link>{" "}
                        page.
                      </span>
                    )}
                  </div>
                </CardContent>
              </Card>

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
                  {state.echo && modelsFor(state.rows, "stt").length === 0 && (
                    <p className="text-xs text-amber-600 dark:text-amber-400">
                      No model is ticked Transcription (STT), so nothing is
                      transcribed.
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
