# Task models

Pick the models ClawEh uses for single calls, and tick what each one is for.

A task model is never in a conversation and never has tools: each use is one
call with one input and one output. Open **Task models** in the **Models**
group of the sidebar.

## Uses

| Use | Config key | What it does now |
|---|---|---|
| **Text** | `text` | Recorded for agents to use in a later release. |
| **Images** | `image` | Recorded for agents to use in a later release. |
| **Speech (TTS)** | `tts` | Recorded for agents to use in a later release. Shows **Voice** and **Format**, the defaults a speech call uses (`tts_defaults`). |
| **Transcription (STT)** | `stt` | Turns incoming voice messages into text. See [Voice messages and transcription](../channels/voice.md). |
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
| Text, Classify | Any provider except a person. |
| Transcription (STT) | `openai-chat` and `openai-responses` providers (an OpenAI-style `/audio/transcriptions` endpoint). The provider also needs a **Base URL** and an **API Key**; a model without them is skipped when voice is transcribed. |
| Vision | HTTP providers. For a CLI provider it is not known, so it is not marked. |
| Images, Speech (TTS) | Not known from the protocol, so never marked. |

A model that is disabled on the Models page, or no longer there, is marked too
and is not used.

## Vision

A model sees images only when its **Vision (tool-result images)** setting is
not **Off** (see [Providers, models and fallback](./concepts.md#vision-models)).
When an agent's model is set to **Off** and it receives an image, ClawEh sends
the image to the models ticked **Vision**, in order, and gives the agent the
text description. With no Vision model, images are dropped for such models.

## Settings

| Setting | Config key | Notes |
|---|---|---|
| **Text in results (characters)** | `result_text_chars` | How much text a task result carries; longer text is cut. Blank or 0 uses the default, 8000. |
| **Echo transcription** | `echo_transcription` | Sends the transcript of a voice message back to the sender before the agent answers. Works only with a Transcription (STT) model. |

## In config.json

```json
"task_models": {
  "result_text_chars": 8000,
  "echo_transcription": false,
  "models": [
    { "model": "whisper", "stt": true },
    { "model": "fast", "text": true, "vision": true, "classify": true },
    { "model": "tts-1", "tts": true, "tts_defaults": { "voice": "alloy", "format": "mp3" } }
  ]
}
```

`model` is a `model_name` from the Models page. Renaming a model on the Models
page renames it here; a model listed here can't be deleted until it is taken
off. A misspelt use is reported as an unknown key at start. The environment
variable `CLAW_TASK_MODELS_ECHO_TRANSCRIPTION` sets **Echo transcription**.

## Check Up

[Check Up](../admin/checkup.md) shows **Speech to text** and **Vision** with
"No model." while nothing is ticked for them, and the full report has a
**Task models** table: each model, its uses, and the uses its provider can't
serve.

## Related pages

- [Providers, models and fallback](./concepts.md)
- [Voice messages and transcription](../channels/voice.md)
- [Classifiers](../agents/classifiers.md)
