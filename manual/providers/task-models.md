# Task models

Pick the models ClawEh uses for single calls, one ordered list for each use.

A task model is never in a conversation and never has tools: each use is one
call with one input and one output. Open **Task models** in the **Models**
group of the sidebar.

## The lists

The page has six lists, in this order. Each list's order is its fallback
order: the top model is tried first, and when it fails the next one is.

| List | Config key | What it does |
|---|---|---|
| **Generate text** | `text` | Writes text from a prompt: writes, translates or summarises for an agent. See [Agents using task models](#agents-using-task-models). |
| **Generate images** | `image` | Makes or edits images from a prompt, for an agent. Each row has **Images through** (`image_api`): **Chat (image output)** (`chat`, the default: a chat call that returns images, as Gemini image models do) or **Images API** (`images`: OpenAI-style `/images/generations` and `/images/edits`; OpenRouter's `/images`, which can't edit). |
| **Text to speech** | `tts` | Speaks text aloud, for an agent. Each row has **Voice** and **Format**, the defaults a speech call uses (`tts_defaults`). |
| **Transcription** | `stt` | Turns incoming voice messages and audio into text, and transcribes audio for an agent. See [Voice messages and transcription](../channels/voice.md). While it is empty, voice messages are not transcribed. |
| **Describe images** | `vision` | Describes images in text for an agent whose model can't see them. See [Describe images](#describe-images). While it is empty, images are not described. |
| **Classification** | `classify` | The models an agent's **Classifier model** can be. See [Classifiers](../agents/classifiers.md). While it is empty, classifiers can't run. |

A model can be in several lists, at most once in each, and in a different
place in each: a fast model can be first for Classification and last for
Generate text.

## Set it up

1. Add the model on the [Models](./concepts.md) page first. Its provider gives
   the endpoint and the API key.
2. On **Task models**, in the list you want, choose it in **Model to add**
   and click **Add model**. Each list offers the enabled models it does not
   hold yet, by name.
3. Use the arrows (**Move up**, **Move down**) to order the list, and the bin
   (**Remove**) to take a model off that list only.

The page saves by itself ("Saved ✓") and the change applies without a
restart.

A model whose provider can't serve a list's use is marked in that list, for
example "fast's provider can't do this." under its row in Transcription. This
is judged from the provider's protocol:

| List | Providers that can serve it |
|---|---|
| Generate text, Classification | Any provider except a person, `gemini` and `elevenlabs`. |
| Transcription, Text to speech | `openai-chat` and `openai-responses` providers (OpenAI-style `/audio` endpoints; OpenRouter's transcription is recognised by its address), and `gemini` and `elevenlabs` providers. An OpenAI-style provider also needs a **Base URL**; every one needs an **API Key**; a model without them is skipped. |
| Describe images | HTTP providers. For a CLI provider it is not known, so it is not marked. |
| Generate images | `openai-chat` and `openai-responses` providers. For others it is not known, so it is not marked. |

A model that is disabled on the Models page, or no longer there, is marked too
and is not used.

## Transcription and speech

A transcription or speech model is an ordinary entry on the Models page: a
provider and a model ID. It is not a channel, and no agent chats with it
unless you add it to that agent's models.

### Which address is called

The provider decides where the audio goes. For an `openai-chat` or
`openai-responses` provider, ClawEh uses only its **Base URL** and **API
Key**:

| List | Request |
|---|---|
| Transcription | `POST <Base URL>/audio/transcriptions` |
| Text to speech | `POST <Base URL>/audio/speech` |

For an `openrouter.ai` Base URL the audio is sent in OpenRouter's format;
for any other address, in OpenAI's. The provider's chat settings (its
protocol, strict mode, reasoning options) are not used for these calls, so
several providers with the same Base URL reach the same endpoint. Pick the
one whose API key should be billed.

### Which model ID to enter

The model ID must be one the provider's service offers for that use:

| Service | Base URL | Transcription model | Speech model |
|---|---|---|---|
| OpenRouter | `https://openrouter.ai/api/v1` | `openai/whisper-large-v3-turbo` | see OpenRouter's model list |
| OpenAI | `https://api.openai.com/v1` | `whisper-1`, `gpt-4o-transcribe` | `tts-1`, `gpt-4o-mini-tts` |
| Groq | `https://api.groq.com/openai/v1` | `whisper-large-v3` | see Groq's model list |
| A local Whisper server with an OpenAI-style API | its address, ending in `/v1` | the name the server gives its model | — |

The same model on another service has another ID: `openai/whisper-large-v3`
is OpenRouter's name, and on OpenAI the model is `whisper-1`. A provider
whose service has no audio endpoint (DeepSeek, for example) is not marked on
this page, because only its protocol is known; its calls fail and the next
model in the list is tried.

### Example: OpenRouter transcription

1. **Models** page: add a model named, for example, `OR Whisper`, with
   provider your OpenRouter provider and model `openai/whisper-large-v3-turbo`.
2. **Task models** page: add `OR Whisper` to **Transcription**.
3. Send a voice message to an agent. Check Up's **Speech to text** row no
   longer says "No model in Transcription."

### Gemini and ElevenLabs

To use Google's Gemini API or ElevenLabs for speech, add a provider on the
**Providers** page with the protocol `gemini` or `elevenlabs` and its **API
Key**; the **Base URL** may be left blank for the vendor's own. Then add the
model (for example a Gemini TTS model, or ElevenLabs' `scribe_v1` or a voice
model) on the Models page and add it to **Text to speech** or
**Transcription** here. A model on one of these providers can't chat: an
agent can't use it as its own model. Gemini returns raw audio, which ClawEh
saves as a WAV file.

## Suggested models on OpenRouter

Through one OpenRouter provider you can try models from several vendors for
each list. The IDs below are from OpenRouter's model list as of November
2026; models change often, so check
[OpenRouter's list](https://openrouter.ai/models) for what is current and
what each costs. Add each on the **Models** page with your OpenRouter
provider and the ID as the model, then add it to its list here.

| List | Model IDs (vendor) | Notes |
|---|---|---|
| **Generate text** | `anthropic/claude-haiku-5.5` (Anthropic), `deepseek/deepseek-v4.1-flash` (DeepSeek), `mistralai/mistral-small-2603` (Mistral), `~google/gemini-flash-latest` (Google) | |
| **Generate images** | `google/gemini-nano-banana-2.1` (Google), `openai/gpt-image-2.6-flare` (OpenAI) | Nano Banana: **Images through: Chat (image output)**; it makes and edits images. GPT Image: **Images through: Images API**; through OpenRouter it makes images but can't edit them, so put Nano Banana first. Put both on an `openai-chat` OpenRouter provider; see [Images on OpenRouter](#images-on-openrouter). |
| **Text to speech** | `google/gemini-3.8-flash-tts` (Google), `elevenlabs/eleven-flash-v2.5` (ElevenLabs), `mistralai/voxtral-mini-tts-2603` (Mistral), `hexgrad/kokoro-82m` (Kokoro, open model) | Each model has its own voices; see [Choosing a voice](#choosing-a-voice). |
| **Transcription** | `openai/whisper-large-v3-turbo` (OpenAI, recommended), `mistralai/voxtral-mini-transcribe` (Mistral), `elevenlabs/scribe-v2` (ElevenLabs), `google/gemini-3.5-transcribe` (Google) | |
| **Describe images** | `google/gemini-3.8-flash` or `~google/gemini-flash-latest` (Google), `anthropic/claude-haiku-5.5` (Anthropic), `qwen/qwen3.8-omni-flash` (Qwen) | |
| **Classification** | `google/gemini-3.8-flash` or `~google/gemini-flash-latest` (Google), `deepseek/deepseek-v4.1-flash` (DeepSeek), `mistralai/mistral-small-2603` (Mistral), `anthropic/claude-haiku-5.5` (Anthropic) | Every item costs one call, so a small, fast model is usually enough; put the cheapest first. |

An ID starting with `~`, such as `~google/gemini-flash-latest`, is an
OpenRouter alias for the vendor's newest model of that kind: it moves to a new
model when the vendor releases one, without a change on your side. Use the
fixed ID instead when you want results to stay the same.

A model that only writes text can't make images, even if it can read them:
`google/gemini-3.8-flash` and `~google/gemini-flash-latest` are good for
Describe images, Generate text and Classification but not for Generate
images. OpenRouter shows a model's outputs and any retirement date on its
page; a model being retired should be replaced before that date.

Putting two models from different vendors in a list also gives you a
fallback when one vendor is down.

### Images on OpenRouter

Use an OpenRouter provider with the `openai-chat` protocol for image models,
not `openai-responses`:

- **Chat (image output)** is a chat call; ClawEh reads the images from the
  reply's `images` list, which is how OpenRouter returns them on the chat
  protocol. An `openai-responses` provider looks for OpenAI's own image
  results instead and finds none. Use it for models that answer with images
  and text, such as Gemini's image models. It can also edit: the agent's
  input images are sent with the prompt.
- **Images API** sends the request to OpenRouter's `/images` endpoint, using
  only the provider's Base URL and API key. Use it for models that only
  output images, such as OpenAI's `gpt-image` models. OpenRouter's endpoint
  can't edit, so an edit request to such a model fails and the next model in
  Generate images is tried.

### Choosing a voice

Each row of **Text to speech** has a **Voice** field and a **Format** field.
**Voice** is the voice the model speaks in; **Format** is the audio format
(for example `mp3`), and is left to the model's default when blank. Both are
saved as that entry's `tts_defaults`. An agent can ask for another voice or
format on a single call (`voice` and `format` in `agent_spawn`).

The voice must be one the model offers, and each model has its own set:

| Model | Example voices |
|---|---|
| `google/gemini-3.8-flash-tts` | `Kore`, `Puck`, `Leda`, `Charon`, `Aoede` |
| `elevenlabs/eleven-flash-v2.5` | `sarah`, `george`, `alice`, `brian`, `lily` |
| `mistralai/voxtral-mini-tts-2603` | `en_paul_neutral`, `gb_oliver_neutral`, and moods such as `en_paul_cheerful` |
| `hexgrad/kokoro-82m` | `af_heart`, `af_bella`, `am_adam`, `bf_emma`, `bm_daniel` |

The full list for each model is on its page on OpenRouter. A voice the model
doesn't have makes the call fail, and the next model in Text to speech is
tried.

## Describe images

A model sees images only when its **Vision (tool-result images)** setting is
not **Off** (see [Providers, models and fallback](./concepts.md#vision-models)).
When an agent's model is set to **Off** and it receives an image, ClawEh sends
the image to the models in **Describe images**, in order, and gives the agent
the text description. While the list is empty, images are dropped for such
models.

## Agents using task models

An agent calls a task model with `agent_spawn` and the `kind` argument. Each
kind uses its list:

| `kind` | List | `task` is | Inputs | Output |
|---|---|---|---|---|
| `text` | Generate text | The prompt. | Text files are added to the prompt; images are attached. | A `.md` file, and the text in the result. |
| `image` | Generate images | The prompt. | Images to edit. | The image files. |
| `tts` | Text to speech | The text to speak. | None; `voice` and `format` override the entry's defaults. | An audio file. |
| `stt` | Transcription | An optional hint (names, terms). | Exactly one audio file. | A `.md` transcript, and the text in the result. |

- Inputs are `files` (up to 10 paths in the agent's workspace, read with its
  file permission, like `msg_send_file`: the agent needs **file_read_lines**
  or **file_read_bytes**, and each file must be within
  `agents.defaults.max_media_size`) or `media` (attachments from the
  conversation).
- `model` names one model in the kind's list; without it, the list is tried
  in order, skipping any model in cooldown and moving on when one fails.
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
"Alice is not allowed to use task models for images." A kind whose list is
empty is noted under its box, for example "No model in Generate images. Add
one on Task models." The agent also needs `agent_spawn` among its tools:
without it the boxes do nothing, and the card says "Needs the agent_spawn
tool." To pass input `files`, it also needs a file-reading tool
(**file_read_lines** or **file_read_bytes**). Check Up lists each agent's
allowed kinds, marked "(ignored: no agent_spawn)" when it lacks the tool.

## Settings

| Setting | Config key | Notes |
|---|---|---|
| **Text in results (characters)** | `result_text_chars` | How much text a task result gives the agent; longer text is cut, and the whole text is in the saved file. Blank or 0 uses the default, 8000. |
| **Echo transcription** | `echo_transcription` | Sends the transcript of a voice message back to the sender before the agent answers. Works only with a model in Transcription; the page says so while that list is empty. |

## In config.json

```json
"task_models": {
  "result_text_chars": 8000,
  "echo_transcription": false,
  "text": [{ "model": "fast" }, { "model": "smart" }],
  "image": [
    { "model": "nano-banana" },
    { "model": "gpt-image", "image_api": "images" }
  ],
  "tts": [{ "model": "tts-1", "tts_defaults": { "voice": "alloy", "format": "mp3" } }],
  "stt": [{ "model": "whisper" }],
  "vision": [{ "model": "fast" }],
  "classify": [{ "model": "fast" }]
}
```

Each entry's `model` is a `model_name` from the Models page. `image_api` is
only for `image` entries and `tts_defaults` only for `tts` entries; on
another list either is reported as an unknown key at start and dropped on
the next save. A model listed twice in one list is refused. Renaming a model
on the Models page renames it in every list; a model in a list can't be
deleted until it is taken off, and the refusal names the list (for example
`task_models.tts[tts-1]`). The environment variable
`CLAW_TASK_MODELS_ECHO_TRANSCRIPTION` sets **Echo transcription**.

## Check Up

[Check Up](../admin/checkup.md) shows **Speech to text** with "No model in
Transcription." and **Vision** with "No model in Describe images." while those
lists are empty, and marks them with "No usable model: …", naming each model
in the list and why, when the list has models but none can be used (disabled,
not on the Models page, a provider that can't serve it or lacks its key). A
disabled transcription model raises no alert. The full report has a **Task
models** table: one row per list with its models in order and the ones whose
provider can't serve it.

## Related pages

- [Providers, models and fallback](./concepts.md)
- [Voice messages and transcription](../channels/voice.md)
- [Classifiers](../agents/classifiers.md)
