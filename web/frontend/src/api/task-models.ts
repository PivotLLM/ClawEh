// API client for the Task models page: the models ClawEh uses for single
// calls without tools, in one ordered list per use.
import { errorMessage } from "./error-message"

/** The uses, one list each, in the page's order. */
export const TASK_USES = [
  "text",
  "image",
  "tts",
  "stt",
  "vision",
  "classify",
] as const
export type TaskUse = (typeof TASK_USES)[number]

export interface TTSDefaults {
  voice?: string
  format?: string
}

/** One entry of a list: a model_name, and the settings its list has
 *  (image_api on image, tts_defaults on tts). */
export interface TaskModelEntry {
  model: string
  /** How a model in Generate images is called: "chat" (default) or
   *  "images". */
  image_api?: "chat" | "images"
  tts_defaults?: TTSDefaults
}

/** The six lists, each in fallback order. */
export type TaskModelLists = Record<TaskUse, TaskModelEntry[]>

export type TaskModelsConfig = {
  result_text_chars?: number
  echo_transcription?: boolean
} & TaskModelLists

// TASK_MODEL_DEFAULTS mirrors config.DefaultTaskResultTextChars and the list
// order (config.TaskUses) in the backend. Both are checked against
// config/testdata/task_model_defaults.json.
export const TASK_MODEL_DEFAULTS = {
  result_text_chars: 8000,
  lists: [...TASK_USES],
}

/** Whether a model's provider can serve a use, judged from its protocol. */
export type UseSupport = "yes" | "no" | "unknown"

/** A model on the Models page, as a list may add it. */
export interface TaskModelOption {
  name: string
  enabled: boolean
  support: Partial<Record<TaskUse, UseSupport>>
}

export interface TaskModelsResponse {
  task_models: TaskModelsConfig
  defaults: { result_text_chars: number }
  models: TaskModelOption[]
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(path, options)
  if (!res.ok) {
    throw new Error(await errorMessage(res))
  }
  return res.json() as Promise<T>
}

/** emptyLists is every list, empty. */
export function emptyLists(): TaskModelLists {
  return Object.fromEntries(
    TASK_USES.map((u) => [u, [] as TaskModelEntry[]]),
  ) as TaskModelLists
}

// getTaskModels normalises the lists at the boundary: a missing or null list
// is an empty one, and every consumer expects an array.
export const getTaskModels = async (): Promise<TaskModelsResponse> => {
  const data = await request<Partial<TaskModelsResponse>>("/api/task-models")
  const tm = (data.task_models ?? {}) as Partial<TaskModelsConfig>
  const lists = emptyLists()
  for (const use of TASK_USES) {
    const list = tm[use]
    if (Array.isArray(list)) lists[use] = list
  }
  return {
    task_models: { ...tm, ...lists },
    defaults: data.defaults ?? {
      result_text_chars: TASK_MODEL_DEFAULTS.result_text_chars,
    },
    models: (data.models ?? []).map((m) => ({
      ...m,
      support: m.support ?? {},
    })),
  }
}

export const saveTaskModels = (taskModels: TaskModelsConfig) =>
  request<{ status: string }>("/api/task-models", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(taskModels),
  })
