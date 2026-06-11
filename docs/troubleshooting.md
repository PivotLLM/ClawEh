# Troubleshooting

## Telegram bot doesn't respond to messages in a group

**Symptom:** The bot works in private chats and may respond once in a group
(e.g. to a command or mention), but then ignores ordinary group messages.
Nothing appears in the logs when those messages are sent.

**Cause:** A Telegram bot has **Group Privacy enabled** by default. While it is
on, Telegram only delivers `/commands`, `@mentions` of the bot, and replies to
the bot's own messages to the bot — ordinary group messages are filtered out
server-side and never reach ClawEh, so they never appear in the logs.

**Fix:** In [@BotFather](https://t.me/BotFather): `/mybots` → select the bot →
**Bot Settings** → **Group Privacy** → **Turn off**. The change takes effect
immediately; you do **not** need to remove and re-add the bot from the group.

See [docs/telegram.md](telegram.md) for full setup details.

## "model ... not found in model_list" or OpenRouter "free is not a valid model ID"

**Symptom:** You see either:

- `Error creating provider: model "openrouter/free" not found in model_list`
- OpenRouter returns 400: `"free is not a valid model ID"`

**Cause:** The `model` field in your `model_list` entry is what gets sent to the API. For OpenRouter you must use the **full** model ID, not a shorthand.

- **Wrong:** `"model": "free"` → OpenRouter receives `free` and rejects it.
- **Right:** `"model": "openrouter/free"` → OpenRouter receives `openrouter/free` (auto free-tier routing).

**Fix:** In `~/.claw/config.json` (or your config path):

1. **agents.defaults.model** must match a `model_name` in `model_list` (e.g. `"openrouter-free"`).
2. That entry’s **model** must be a valid OpenRouter model ID, for example:
   - `"openrouter/free"` – auto free-tier
   - `"google/gemini-2.0-flash-exp:free"`
   - `"meta-llama/llama-3.1-8b-instruct:free"`

Example snippet:

```json
{
  "agents": {
    "defaults": {
      "model": "openrouter-free"
    }
  },
  "model_list": [
    {
      "model_name": "openrouter-free",
      "model": "openrouter/free",
      "api_key": "sk-or-v1-YOUR_OPENROUTER_KEY",
      "api_base": "https://openrouter.ai/api/v1"
    }
  ]
}
```

Get your key at [OpenRouter Keys](https://openrouter.ai/keys).
