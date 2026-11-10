# Voice messages and transcription

Have voice messages turned into text before they reach the agent, using a speech-to-text service you choose.

Without a transcription service, a voice message reaches the agent as an audio
file it usually cannot listen to. With one, the agent gets the words.

## Where voice comes from

| Source | What ClawEh receives |
|---|---|
| [Telegram](./telegram.md) | Voice notes and audio files. |
| [Discord](./discord.md) | Audio files attached to a message. |
| [Slack](./slack.md), [Matrix](./matrix.md), [SecMsg](./secmsg.md) | Audio files and voice messages sent as attachments. |
| [Devices](./devices.md) (Rabbit R1, voice apps) | Text, or an audio clip sent as an attachment. A device that turns speech into text itself (the Rabbit R1) needs no transcription service; one that sends audio does. |

A file counts as audio by its type (`audio/...`) or its name (`.mp3`, `.wav`,
`.ogg`, `.oga`, `.opus`, `.m4a`, `.flac`, `.aac`, `.wma`).

## Set up a transcription service

Transcription uses the models in the **Transcription** list on the
[Task models](../providers/task-models.md) page. The model's provider gives the
endpoint and the API key, so the same provider can serve both chat models and
transcription. The audio goes to `<Base URL>/audio/transcriptions`; several
providers with the same Base URL reach the same endpoint, so pick the one
whose key should be billed (see
[Which address is called](../providers/task-models.md#which-address-is-called)).

1. On the **Providers** page, make sure the service has a provider with an
   **API Key**. The built-in catalogue already has **Groq**, **OpenAI** and
   **OpenRouter Chat**; for any other service with an OpenAI-compatible
   `/audio/transcriptions` endpoint, click **Add Provider**, pick the
   `openai-chat` protocol and enter its **Base URL** and **API Key** (see
   [Providers and models](../providers/index.md)).
2. On the **Models** page, add a model on that provider. The model id is the
   service's speech model, for example:

   | Provider endpoint | Model id |
   |---|---|
   | `https://api.groq.com/openai/v1` | `whisper-large-v3` |
   | `https://api.openai.com/v1` | `whisper-1` |
   | `https://openrouter.ai/api/v1` | `openai/whisper-large-v3-turbo` |

3. On **Task models**, add the model to **Transcription**.

The page saves by itself ("Saved ✓"), and the change applies without a
restart. Send the agent a voice message to check.

A model whose provider can't serve speech (see
[Task models](../providers/task-models.md#which-address-is-called)), or lacks
the **Base URL** or **API Key** it needs, is skipped, and ClawEh raises the **Speech backend
not usable** [alert](../admin/alerts.md). The provider's proxy setting is not
used for transcription.

## Several models

The models in **Transcription** are tried in the order of that list. If one fails (the service is down, or refuses the key), the next usable
one is tried. Silence is not a failure: an empty transcript does not move on to
the next model.

To stop transcribing, remove every model from **Transcription**.

## What the agent sees

The voice message is replaced by its text, written as `[voice: …]` in the
message. The audio file is not passed on. If every model fails, the agent
gets `[voice: ]` with no text, and the failure is in the
[log](../admin/logs.md).

## Show the transcript to the sender

ClawEh can send the transcript back to the chat first, so the person sees what
was understood: "Transcript: …", or "No voice detected in the audio" when
nothing came out. It is off by default. Turn on **Echo transcription** on the
[Task models](../providers/task-models.md) page (`task_models.echo_transcription`,
or the environment variable `CLAW_TASK_MODELS_ECHO_TRANSCRIPTION=true` for the
service).

## Costs and keys

- The audio is sent to the service you choose, which charges for it (usually
  by length) under its own pricing. ClawEh does not count or limit it.
- When the service refuses the key or the account (no credit), ClawEh raises
  the **Voice transcription rejected** [alert](../admin/alerts.md), and voice
  messages are not transcribed until it is fixed.
- The key is the provider's, stored in `config.json` under `providers`. The
  models are under `task_models.stt`, each naming a model from the Models
  page. Like other secrets, the key can be a reference such as
  `env:GROQ_API_KEY` (see
  [Configuration](../reference/configuration.md#keeping-secrets-out-of-the-file)).
- [Check Up](../admin/checkup.md) shows **Speech to text: No model in Transcription.** while
  that list is empty, and the full report lists each task model list in
  order.

## Agents that represent a person

Voice messages to a [human agent](../agents/human-agents.md) are never
transcribed.

## Related pages

- [Channels overview](./index.md)
- [Devices](./devices.md)
- [Task models](../providers/task-models.md)
- [Alerts](../admin/alerts.md)
