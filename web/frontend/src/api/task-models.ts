// API client for the Task models page: the models ClawEh uses for single
// calls without tools, each ticked for the uses it serves.
import { errorMessage } from "./error-message"

/** One use a task model can be ticked for, in the page's checkbox order. */
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

/** One row of the page: a model_name and its ticked uses. */
export type TaskModel = { model: string; tts_defaults?: TTSDefaults } & Partial<
  Record<TaskUse, boolean>
>

export interface TaskModelsConfig {
  result_text_chars?: number
  echo_transcription?: boolean
  models: TaskModel[]
}

// TASK_MODEL_DEFAULTS mirrors config.DefaultTaskResultTextChars in the
// backend. Both are checked against config/testdata/task_model_defaults.json.
export const TASK_MODEL_DEFAULTS = {
  result_text_chars: 8000,
} as const

/** Whether a model's provider can serve a use, judged from its protocol. */
export type UseSupport = "yes" | "no" | "unknown"

/** A model on the Models page, as the Task models page may add it. */
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

// getTaskModels normalises the lists at the boundary: Go may encode an empty
// slice as null, and every consumer expects an array.
export const getTaskModels = async (): Promise<TaskModelsResponse> => {
  const data = await request<Partial<TaskModelsResponse>>("/api/task-models")
  return {
    task_models: {
      ...data.task_models,
      models: data.task_models?.models ?? [],
    },
    defaults: data.defaults ?? { ...TASK_MODEL_DEFAULTS },
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
