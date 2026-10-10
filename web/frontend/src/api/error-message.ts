// The sentence a failed API call shows the operator. The server answers a
// refused configuration save with JSON {"errors": [...]}, one sentence per
// refusal; other failures may carry {"error": "..."} or plain text.
export async function errorMessage(res: Response): Promise<string> {
  const fallback = `API error: ${res.status} ${res.statusText}`
  let text = ""
  try {
    text = (await res.text()).trim()
  } catch {
    return fallback
  }
  if (text === "") {
    return fallback
  }
  try {
    const body = JSON.parse(text) as { error?: unknown; errors?: unknown }
    if (Array.isArray(body.errors)) {
      const sentences = body.errors
        .filter((e): e is string => typeof e === "string" && e.trim() !== "")
        .map(asSentence)
      if (sentences.length > 0) {
        return sentences.join(" ")
      }
    }
    if (typeof body.error === "string" && body.error.trim() !== "") {
      return body.error
    }
    return fallback
  } catch {
    return text // a plain-text answer is the message itself
  }
}

// asSentence ends s with a full stop unless it already ends a sentence, so
// several refusals read as separate sentences.
function asSentence(s: string): string {
  const t = s.trim()
  return /[.!?]$/.test(t) ? t : `${t}.`
}
