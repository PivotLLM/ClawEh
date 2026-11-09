// Pure helpers behind the Task models page, kept apart so they can be tested
// without rendering.
import {
  TASK_USES,
  type TaskModel,
  type TaskModelOption,
  type TaskModelsConfig,
  type TaskUse,
} from "@/api/task-models"

/** The checkbox label of each use, in page order. */
export const USE_LABELS: Record<TaskUse, string> = {
  text: "Text",
  image: "Images",
  tts: "Speech (TTS)",
  stt: "Transcription (STT)",
  vision: "Vision",
  classify: "Classify",
}

/** usesOf lists the uses a row is ticked for, in page order. */
export function usesOf(row: TaskModel): TaskUse[] {
  return TASK_USES.filter((u) => row[u] === true)
}

/** setUse ticks or unticks one use; unticking Speech (TTS) keeps its
 *  defaults so ticking it again brings them back until the page is left. */
export function setUse(row: TaskModel, use: TaskUse, on: boolean): TaskModel {
  const next = { ...row }
  if (on) next[use] = true
  else delete next[use]
  return next
}

/** addModel appends a model with no use ticked, unless it is already on the
 *  page (a model is listed at most once). */
export function addModel(rows: TaskModel[], name: string): TaskModel[] {
  if (!name || rows.some((r) => r.model === name)) return rows
  return [...rows, { model: name }]
}

/** moveRow moves row i one place up (-1) or down (+1); out of range is a
 *  no-op. */
export function moveRow(
  rows: TaskModel[],
  i: number,
  dir: -1 | 1,
): TaskModel[] {
  const j = i + dir
  if (i < 0 || i >= rows.length || j < 0 || j >= rows.length) return rows
  const next = [...rows]
  ;[next[i], next[j]] = [next[j], next[i]]
  return next
}

/** availableModels are the Models page names not yet on the page. */
export function availableModels(
  rows: TaskModel[],
  options: TaskModelOption[],
): string[] {
  return options
    .map((o) => o.name)
    .filter((n) => !rows.some((r) => r.model === n))
}

/** unsupportedUses are the ticked uses the model's provider cannot serve. */
export function unsupportedUses(
  row: TaskModel,
  option: TaskModelOption | undefined,
): TaskUse[] {
  if (!option) return []
  return usesOf(row).filter((u) => option.support[u] === "no")
}

/** modelsFor lists the models ticked for use, in page order. */
export function modelsFor(rows: TaskModel[], use: TaskUse): string[] {
  return rows.filter((r) => r[use] === true).map((r) => r.model)
}

/** parseResultChars reads the "Text in results" field: blank is the default
 *  (0), a whole number of 0 or more is itself, anything else is null. */
export function parseResultChars(text: string): number | null {
  const t = text.trim()
  if (t === "") return 0
  if (!/^\d+$/.test(t)) return null
  return Number(t)
}

/** taskModelsPayload is the block the page saves: TTS defaults only on a
 *  model ticked Speech (TTS) and only when one is set, the limit left out
 *  when it is the default. */
export function taskModelsPayload(
  rows: TaskModel[],
  resultTextChars: number,
  echoTranscription: boolean,
): TaskModelsConfig {
  const models = rows.map((r) => {
    const out: TaskModel = { model: r.model }
    for (const u of usesOf(r)) out[u] = true
    const voice = r.tts_defaults?.voice?.trim() ?? ""
    const format = r.tts_defaults?.format?.trim() ?? ""
    if (r.tts === true && (voice || format)) {
      out.tts_defaults = {
        ...(voice ? { voice } : {}),
        ...(format ? { format } : {}),
      }
    }
    return out
  })
  const block: TaskModelsConfig = { models }
  if (resultTextChars > 0) block.result_text_chars = resultTextChars
  if (echoTranscription) block.echo_transcription = true
  return block
}
