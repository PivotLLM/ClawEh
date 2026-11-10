# Task models

Pick the models ClawEh uses for single calls, and tick what each one is for.

A task model is never in a conversation and never has tools: each use is one
call with one input and one output. Open **Task models** in the **Models**
group of the sidebar.

## Uses

| Use | Config key | What it does now |
|---|---|---|
| **Text** | `text` | Writes, translates or summarises for an agent. See [Agents using task models](#agents-using-task-models). |
| **Images** | `image` | Makes or edits images for an agent. Shows **Images through** (`image_api`): **Chat (image output)** (`chat`, the default: a chat call that returns images, as Gemini image models do) or **Images API** (`images`: OpenAI-style `/images/generations` and `/images/edits`; OpenRouter's `/images`, which can't edit). |
| **Speech (TTS)** | `tts` | Speaks text aloud for an agent. Shows **Voice** and **Format**, the defaults a speech call uses (`tts_defaults`). |
| **Transcription (STT)** | `stt` | Turns incoming voice messages into text, and transcribes audio for an agent. See [Voice messages and transcription](../channels/voice.md). |
| **Vision** | `vision` | Describes images for an agent whose model can't see them. See [Vision](#vision). |
| **Classify** | `classify` | The models an agent's **Classifier model** can be. See [Classifiers](../agents/classifiers.md). |

## Set it up

1. Add the model on the [Models](./concepts.md) page first. Its provider gives
   the endpoint and the API key.
2. On **Task models**, choose it under **Models** and click **Add model**.
3. Tick each use it should serve.
4. Use the arrows to order the list. Each use tries its ticked models from
   the top down: when one fails, the next is tried.

The page saves by itself ("Saved ✓") and the change applies without a
restart. A model is listed at most once.

A model whose provider can't serve a ticked use is marked under it, for
example "fast's provider can't do Transcription (STT)." This is judged
from the provider's protocol:

| Use | Providers that can serve it |
|---|---|
| Text, Classify | Any provider except a person, `gemini` and `elevenlabs`. |
| Transcription (STT), Speech (TTS) | `openai-chat` and `openai-responses` providers (OpenAI-style `/audio` endpoints; OpenRouter's transcription is recognised by its address), and `gemini` and `elevenlabs` providers. An OpenAI-style provider also needs a **Base URL**; every one needs an **API Key**; a model without them is skipped. |
| Vision | HTTP providers. For a CLI provider it is not known, so it is not marked. |
| Images | `openai-chat` and `openai-responses` providers. For others it is not known, so it is not marked. |

## Transcription and speech

A transcription or speech model is an ordinary entry on the Models page: a
provider and a model ID. It is not a channel, and no agent chats with it
unless you add it to that agent's models.

### Which address is called

The provider decides where the audio goes. For an `openai-chat` or
`openai-responses` provider, ClawEh uses only its **Base URL** and **API
Key**:

| Use | Request |
|---|---|
| Transcription (STT) | `POST <Base URL>/audio/transcriptions` |
| Speech (TTS) | `POST <Base URL>/audio/speech` |

For an `openrouter.ai` Base URL the audio is sent in OpenRouter's format;
for any other address, in OpenAI's. The provider's chat settings (its
protocol, strict mode, reasoning options) are not used for these calls, so
several providers with the same Base URL reach the same endpoint. Pick the
one whose API key should be billed.

### Which model ID to enter

The model ID must be one the provider's service offers for that use:

| Service | Base URL | Transcription model | Speech model |
|---|---|---|---|
| OpenRouter | `https://openrouter.ai/api/v1` | `openai/whisper-large-v3` | see OpenRouter's model list |
| OpenAI | `https://api.openai.com/v1` | `whisper-1`, `gpt-4o-transcribe` | `tts-1`, `gpt-4o-mini-tts` |
| Groq | `https://api.groq.com/openai/v1` | `whisper-large-v3` | see Groq's model list |
| A local Whisper server with an OpenAI-style API | its address, ending in `/v1` | the name the server gives its model | — |

The same model on another service has another ID: `openai/whisper-large-v3`
is OpenRouter's name, and on OpenAI the model is `whisper-1`. A provider
whose service has no audio endpoint (DeepSeek, for example) is not marked on
this page, because only its protocol is known; its calls fail and the next
ticked model is tried.

### Example: OpenRouter transcription

1. **Models** page: add a model named, for example, `OR Whisper`, with
   provider your OpenRouter provider and model `openai/whisper-large-v3`.
2. **Task models** page: add `OR Whisper` and tick **Transcription (STT)**.
3. Send a voice message to an agent. Check Up's **Speech to text** row no
   longer says "No model."

### Gemini and ElevenLabs

To use Google's Gemini API or ElevenLabs for speech, add a provider on the
**Providers** page with the protocol `gemini` or `elevenlabs` and its **API
Key**; the **Base URL** may be left blank for the vendor's own. Then add the
model (for example a Gemini TTS model, or ElevenLabs' `scribe_v1` or a voice
model) on the Models page and tick **Speech (TTS)** or **Transcription (STT)**
for it here. A model on one of these providers can't chat: an agent can't use
it as its own model. Gemini returns raw audio, which ClawEh saves as a WAV
file.

A model that is disabled on the Models page, or no longer there, is marked too
and is not used.

## Suggested models on OpenRouter

Through one OpenRouter provider you can try models from several vendors for
each use. The IDs below are from OpenRouter's model list as of November 2026;
models change often, so check [OpenRouter's list](https://openrouter.ai/models)
for what is current and what each costs. Add each on the **Models** page with
your OpenRouter provider and the ID as the model, then tick its use here.

| Use | Model IDs (vendor) | Notes |
|---|---|---|
| **Transcription (STT)** | `openai/whisper-large-v3` (OpenAI), `mistralai/voxtral-mini-transcribe` (Mistral), `elevenlabs/scribe-v2` (ElevenLabs), `google/gemini-3.5-transcribe` (Google) | |
| **Speech (TTS)** | `google/gemini-3.8-flash-tts` (Google), `elevenlabs/eleven-flash-v2.5` (ElevenLabs), `mistralai/voxtral-mini-tts-2603` (Mistral), `hexgrad/kokoro-82m` (Kokoro, open model) | Each model has its own voices: set **Voice** to one the model offers. |
| **Images** | `google/gemini-3.1-flash-image` (Google), `openai/gpt-5-image-mini` (OpenAI) | **Images through:** **Chat (image output)**. |
| **Vision** | `google/gemini-3.5-flash-lite` (Google), `anthropic/claude-haiku-5.5` (Anthropic), `qwen/qwen3.8-omni-flash` (Qwen) | |
| **Text** | `anthropic/claude-haiku-5.5` (Anthropic), `deepseek/deepseek-v4.1-flash` (DeepSeek), `mistralai/mistral-small-2603` (Mistral) | |
| **Classify** | `google/gemini-3.5-flash-lite` (Google), `deepseek/deepseek-v4.1-flash` (DeepSeek), `mistralai/mistral-small-2603` (Mistral) | Every item costs one call, so a small, fast model is usually enough. |

Ticking two models from different vendors for a use also gives you a
fallback when one vendor is down.

## Vision

A model sees images only when its **Vision (tool-result images)** setting is
not **Off** (see [Providers, models and fallback](./concepts.md#vision-models)).
When an agent's model is set to **Off** and it receives an image, ClawEh sends
the image to the models ticked **Vision**, in order, and gives the agent the
text description. With no Vision model, images are dropped for such models.

## Agents using task models

An agent calls a task model with `agent_spawn` and the `kind` argument:

| `kind` | `task` is | Inputs | Output |
|---|---|---|---|
| `text` | The prompt. | Text files are added to the prompt; images are attached. | A `.md` file, and the text in the result. |
| `image` | The prompt. | Images to edit. | The image files. |
| `tts` | The text to speak. | None; `voice` and `format` override the model's defaults. | An audio file. |
| `stt` | An optional hint (names, terms). | Exactly one audio file. | A `.md` transcript, and the text in the result. |

- Inputs are `files` (up to 10 paths in the agent's workspace, read with its
  file permission, like `msg_send_file`: the agent needs **file_read_lines**
  or **file_read_bytes**, and each file must be within
  `agents.defaults.max_media_size`) or `media` (attachments from the
  conversation).
- `model` names one task model of that kind; without it, the models ticked
  for the kind are tried in page order, skipping any in cooldown and moving
  on when one fails.
- Output is saved in the agent's workspace (a clone's: its source's) under
  `files/generated/<date>/`. The result gives the paths; images and audio are
  never put in the agent's context, so it sends them with `msg_send_file`.
  Text comes back in the result up to **Text in results (characters)**, cut
  after that with the file named.
- `mode` `wait` returns the result; `callback` (the default) runs it in the
  background and reports back, like a sub-agent (see
  [Sub-agents](../agents/subagents.md)). A task model has no tools and adds no
  sub-agent level; it is refused at the maximum depth like any spawn.
- `agent_id` and `character_card` can't be used with `kind`.
- A reported cost counts toward `agents.defaults.daily_spend_alert_usd`, each
  attempt is logged, and each call is in the [audit log](../admin/audit.md) as
  **Task model**.

### Allow task models

An agent may use only the kinds ticked under **Allow task models** on its
card on the **Agents** page (`agents.list[].task_models`: `text`, `image`,
`tts`, `stt`; all off by default). Any other kind is refused, for example
"Alice is not allowed to use task models for images." A kind with no model
ticked here is noted under its box. The agent also needs `agent_spawn` among
its tools: without it the boxes do nothing, and the card says "Needs the
agent_spawn tool." To pass input `files`, it also needs a file-reading tool
(**file_read_lines** or **file_read_bytes**). Check Up lists each agent's
allowed kinds, marked "(ignored: no agent_spawn)" when it lacks the tool.

## Settings

| Setting | Config key | Notes |
|---|---|---|
| **Text in results (characters)** | `result_text_chars` | How much text a task result gives the agent; longer text is cut, and the whole text is in the saved file. Blank or 0 uses the default, 8000. |
| **Echo transcription** | `echo_transcription` | Sends the transcript of a voice message back to the sender before the agent answers. Works only with a Transcription (STT) model. |

## In config.json

```json
"task_models": {
  "result_text_chars": 8000,
  "echo_transcription": false,
  "models": [
    { "model": "whisper", "stt": true },
    { "model": "fast", "text": true, "vision": true, "classify": true },
    { "model": "tts-1", "tts": true, "tts_defaults": { "voice": "alloy", "format": "mp3" } },
    { "model": "gpt-image", "image": true, "image_api": "images" }
  ]
}
```

`model` is a `model_name` from the Models page. Renaming a model on the Models
page renames it here; a model listed here can't be deleted until it is taken
off. A misspelt use is reported as an unknown key at start. The environment
variable `CLAW_TASK_MODELS_ECHO_TRANSCRIPTION` sets **Echo transcription**.

## Check Up

[Check Up](../admin/checkup.md) shows **Speech to text** and **Vision** with
"No model." while nothing is ticked for them, and marks them with "No usable
model: …", naming each ticked model and why, when models are ticked but none
can be used (disabled, not on the Models page, a provider that can't serve
it or lacks its key). A disabled transcription model raises no alert. The
full report has a
**Task models** table: each model, its uses, and the uses its provider can't
serve.

## Related pages

- [Providers, models and fallback](./concepts.md)
- [Voice messages and transcription](../channels/voice.md)
- [Classifiers](../agents/classifiers.md)
