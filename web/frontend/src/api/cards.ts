// The character-card library (GET/POST/DELETE /api/cards): the Cards page and
// the Agents page's card selector.

export interface CardSummary {
  id: string
  name: string
  nickname?: string
  tags: string[]
  creator?: string
  /** Unix seconds. */
  modification_date: number
  /** Ids of the configured agents whose character_card names this card. */
  attached_to: string[]
}

export interface InvalidCard {
  file: string
  reason: string
}

export interface CardsResponse {
  cards: CardSummary[]
  invalid: InvalidCard[]
}

export interface CardImportResponse {
  id: string
  format: string
  card: unknown
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(path, options)
  if (!res.ok) {
    throw new CardsAPIError(res.status, await extractErrorMessage(res))
  }
  return res.json() as Promise<T>
}

/** CardsAPIError carries the status so a 409 (id exists) can offer Replace. */
export class CardsAPIError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.name = "CardsAPIError"
    this.status = status
  }
}

/** The query key the Cards page and the Agents page share for GET /api/cards. */
export const CARDS_QUERY_KEY = ["cards"]

export async function getCards(): Promise<CardsResponse> {
  return request<CardsResponse>("/api/cards")
}

export async function importCard(
  file: File,
  id: string,
  replace: boolean,
): Promise<CardImportResponse> {
  const formData = new FormData()
  formData.set("file", file)
  formData.set("id", id)
  if (replace) formData.set("replace", "true")
  return request<CardImportResponse>("/api/cards/import", {
    method: "POST",
    body: formData,
  })
}

export async function deleteCard(id: string): Promise<{ status: string }> {
  return request<{ status: string }>(`/api/cards/${encodeURIComponent(id)}`, {
    method: "DELETE",
  })
}

/** cardDownloadUrl is the download of the stored card as <id>.json. */
export function cardDownloadUrl(id: string): string {
  return `/api/cards/${encodeURIComponent(id)}`
}

/** defaultCardId suggests an id from a file name: the name without its
 *  extension, lower-cased, runs of other characters replaced by "-", trimmed.
 *  The backend validates the final id. */
export function defaultCardId(fileName: string): string {
  const stem = fileName.replace(/\.[^.]+$/, "")
  return stem
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, "-")
    .replace(/^[-_]+|[-_]+$/g, "")
    .slice(0, 64)
}

async function extractErrorMessage(res: Response): Promise<string> {
  try {
    const body = (await res.json()) as { error?: string }
    if (typeof body.error === "string" && body.error.trim() !== "") {
      return body.error
    }
  } catch {
    // ignore invalid body
  }
  return `API error: ${res.status} ${res.statusText}`
}
