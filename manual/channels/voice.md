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

A speech backend uses a provider from the **Providers** page for its endpoint
and API key. The same provider can serve both chat models and transcription,
or you can add one just for transcription.

1. On the **Providers** page, make sure the service has a provider with an
   **API Key**. The built-in catalogue already has **Groq**, **OpenAI** and
   **OpenRouter Chat**; for any other service with an OpenAI-compatible
   `/audio/transcriptions` endpoint, click **Add Provider**, pick the
   `openai-chat` protocol and enter its **Base URL** and **API Key** (see
   [Providers and models](../providers/index.md)).
2. In the WebUI sidebar, open **Speech** in the **Services** group.
3. Click **Add backend**.
4. Choose the **Provider**.
5. Leave **Model** blank for a known service, or enter the model to use:

   | Provider endpoint | Default model |
   |---|---|
   | `https://api.groq.com/openai/v1` | `whisper-large-v3` |
   | `https://api.openai.com/v1` | `whisper-1` |
   | `https://openrouter.ai/api/v1` | `openai/whisper-large-v3` |

   For any other endpoint the model is required: a backend without one is not
   used, and the page marks it **Not used**.
6. Make sure the switch on the backend says **Enabled**.

The page saves by itself ("Saved ✓"), and the change applies without a
restart. Send the agent a voice message to check.

A backend whose provider has no API key is marked **Not used** and skipped.
A provider that a backend uses cannot be deleted; renaming it carries the
backend along. The provider's proxy setting is not used for transcription.

## Several backends

Backends are tried in the order listed: the first enabled one is marked
**Primary**, the others **Fallback**. If one fails (the service is down, or
refuses the key), the next usable backend is tried. Silence is not a failure:
an empty transcript does not move on to the next backend.

To stop transcribing, turn the backends off or remove them.

## What the agent sees

The voice message is replaced by its text, written as `[voice: …]` in the
message. The audio file is not passed on. If every backend fails, the agent
gets `[voice: ]` with no text, and the failure is in the
[log](../admin/logs.md).

## Show the transcript to the sender

ClawEh can send the transcript back to the chat first, so the person sees what
was understood: "Transcript: …", or "No voice detected in the audio" when
nothing came out. It is off by default and is not on the **Speech** page. To
turn it on, set this in `config.json`:

```json
"voice": {
  "echo_transcription": true
}
```

or set the environment variable `CLAW_VOICE_ECHO_TRANSCRIPTION=true` for the
service.

## Costs and keys

- The audio is sent to the service you choose, which charges for it (usually
  by length) under its own pricing. ClawEh does not count or limit it.
- When the service refuses the key or the account (no credit), ClawEh raises
  the **Voice transcription rejected** [alert](../admin/alerts.md), and voice
  messages are not transcribed until it is fixed.
- The key is the provider's, stored in `config.json` under `providers`. The
  backends themselves are under `voice.stt`, each naming its provider and
  model. Like other secrets, the key can be a reference such as
  `env:GROQ_API_KEY` (see
  [Configuration](../reference/configuration.md#keeping-secrets-out-of-the-file)).
- The [full report](../admin/checkup.md) lists the backends, whether each is
  on, its model, and the chain in use.

## Agents that represent a person

Voice messages to a [human agent](../agents/human-agents.md) are never
transcribed.

## Related pages

- [Channels overview](./index.md)
- [Devices](./devices.md)
- [Alerts](../admin/alerts.md)
