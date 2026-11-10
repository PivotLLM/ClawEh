// Pure helpers behind the Task models page, kept apart so they can be tested
// without rendering.
import {
  TASK_USES,
  type TaskModelEntry,
  type TaskModelLists,
  type TaskModelOption,
  type TaskModelsConfig,
  type TaskUse,
  emptyLists,
} from "@/api/task-models"

/** Each list's section title, in page order (config.TaskUse.Label). */
export const LIST_TITLES: Record<TaskUse, string> = {
  text: "Generate text",
  image: "Generate images",
  tts: "Text to speech",
  stt: "Transcription",
  vision: "Describe images",
  classify: "Classification",
}

/** Each list's one-line description: what goes in and what comes out. */
export const LIST_DESCRIPTIONS: Record<TaskUse, string> = {
  text: "Writes text from a prompt.",
  image: "Makes or edits images from a prompt.",
  tts: "Speaks text aloud.",
  stt: "Turns voice messages and audio into text.",
  vision: "Describes images in text for agents whose model can't see them.",
  classify: "Picks a category for each item a classifier handles.",
}

/** What an empty list means, where ClawEh itself relies on it. */
export const EMPTY_NOTES: Record<TaskUse, string> = {
  text: "No models.",
  image: "No models.",
  tts: "No models.",
  stt: "No models. Voice messages are not transcribed.",
  vision: "No models. Images are not described.",
  classify: "No models. Classifiers can't run.",
}

/** addModel appends a model to a list, unless it is already in it (a
 *  model is in a list at most once). */
export function addModel(
  list: TaskModelEntry[],
  name: string,
): TaskModelEntry[] {
  if (!name || list.some((e) => e.model === name)) return list
  return [...list, { model: name }]
}

/** moveRow moves entry i one place up (-1) or down (+1); out of range is a
 *  no-op. */
export function moveRow(
  list: TaskModelEntry[],
  i: number,
  dir: -1 | 1,
): TaskModelEntry[] {
  const j = i + dir
  if (i < 0 || i >= list.length || j < 0 || j >= list.length) return list
  const next = [...list]
  ;[next[i], next[j]] = [next[j], next[i]]
  return next
}

/** removeRow drops entry i. */
export function removeRow(list: TaskModelEntry[], i: number): TaskModelEntry[] {
  return list.filter((_, idx) => idx !== i)
}

/** availableModels are the enabled Models page names not yet in the list,
 *  sorted by name ignoring case. */
export function availableModels(
  list: TaskModelEntry[],
  options: TaskModelOption[],
): string[] {
  return options
    .filter((o) => o.enabled)
    .map((o) => o.name)
    .filter((n) => !list.some((e) => e.model === n))
    .sort((a, b) => a.localeCompare(b, undefined, { sensitivity: "base" }))
}

/** unsupported reports whether the model's provider cannot serve use. */
export function unsupported(
  use: TaskUse,
  option: TaskModelOption | undefined,
): boolean {
  return option?.support[use] === "no"
}

/** parseResultChars reads the "Text in results" field: blank is the default
 *  (0), a whole number of 0 or more is itself, anything else is null. */
export function parseResultChars(text: string): number | null {
  const t = text.trim()
  if (t === "") return 0
  if (!/^\d+$/.test(t)) return null
  return Number(t)
}

/** entryPayload is one entry as its list saves it: image_api only on image
 *  entries and only when it is not the default (chat), tts_defaults only on
 *  tts entries and only what is set. */
function entryPayload(use: TaskUse, e: TaskModelEntry): TaskModelEntry {
  const out: TaskModelEntry = { model: e.model }
  if (use === "image" && e.image_api === "images") out.image_api = "images"
  if (use === "tts") {
    const voice = e.tts_defaults?.voice?.trim() ?? ""
    const format = e.tts_defaults?.format?.trim() ?? ""
    if (voice || format) {
      out.tts_defaults = {
        ...(voice ? { voice } : {}),
        ...(format ? { format } : {}),
      }
    }
  }
  return out
}

/** taskModelsPayload is the block the page saves: every list, in order,
 *  with only the settings that list has; the limit left out when it is the
 *  default, echo only when on. */
export function taskModelsPayload(
  lists: TaskModelLists,
  resultTextChars: number,
  echoTranscription: boolean,
): TaskModelsConfig {
  const block: TaskModelsConfig = emptyLists()
  for (const u of TASK_USES) block[u] = lists[u].map((e) => entryPayload(u, e))
  if (resultTextChars > 0) block.result_text_chars = resultTextChars
  if (echoTranscription) block.echo_transcription = true
  return block
}
