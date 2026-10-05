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

## "model ... not found in models" or OpenRouter "free is not a valid model ID"

**Symptom:** You see either:

- `Error creating provider: model "openrouter/free" not found in models`
- OpenRouter returns 400: `"free is not a valid model ID"`

**Cause:** The `model` field in your `models` entry is what gets sent to the API. For OpenRouter you must use the **full** model ID, not a shorthand.

- **Wrong:** `"model": "free"` → OpenRouter receives `free` and rejects it.
- **Right:** `"model": "openrouter/free"` → OpenRouter receives `openrouter/free` (auto free-tier routing).

**Fix:** In `~/.claw/config.json` (or your config path):

1. **agents.defaults.model** must match a `model_name` in `models` (e.g. `"openrouter-free"`).
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
  "models": [
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

**Related: `/model` lists an entry with no provider, or the log says
`fallback alias dropped (not enabled in models)`.** An agent's model list
(`agents.list[].models`, or a default, summarization or subagent chain) names a
model that has since been deleted or disabled. At startup and on every config
reload ClawEh removes a reference to a deleted model from `config.json`:
the agent uses the next model in its list, the log says `removed reference to
unknown model from config file`, and one "Agent references a missing model"
alert is raised for it (if the file cannot be written the reference is only
skipped in the running config, and the alert repeats once per restart). The
rest of the change is still applied. A save that would add a new reference to a missing
model is refused and names it; an existing one never blocks a save. Fix it in
the agent's model list (WebUI Agents page) by pointing the slot at a model that
exists and is enabled; the alert clears once the reference is gone.
