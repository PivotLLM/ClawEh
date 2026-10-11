# Providers, models and fallback

This page explains how ClawEh picks a model for each request and what happens when a model fails.

## Providers and models

A **provider** is a named endpoint. It holds what is shared by everything reached through it:

| Field | Config key | Meaning |
|---|---|---|
| Name | `name` | A unique name. Models refer to the provider by it. |
| Protocol | `protocol` | The wire protocol: `openai-chat`, `openai-responses`, `azure`, `anthropic` (same as `anthropic-messages`), one of the CLI protocols (`claude-cli`, `codex-cli`, `antigravity-cli`, `cursor-cli`), or `human`. |
| Base URL | `base_url` | Required for the HTTP protocols. Not used by CLI providers. |
| API Key | `api_key` | The key for a cloud service. CLI providers sign in on their own and need none. |
| HTTP Proxy | `proxy` | Optional outbound proxy. |

A **model** is one way of calling one model through one provider:

| Field (Models page) | Config key | Meaning |
|---|---|---|
| Model Alias / Model Name | `model_name` | The name you and your agents use, for example `Claude Sonnet 4.6`. Agents, the default model list and the memory and vision chains all refer to models by this name. |
| Model Identifier / Model ID | `model` | The id the endpoint expects, exactly as the vendor writes it, for example `gpt-5.5`. No protocol prefix. |
| Provider | `provider` | The provider's name. |
| (switch on the card) | `enabled` | Off means ClawEh ignores the model everywhere. |

Because the alias and the id are separate, you can have several models on the same vendor model with different settings. The built-in catalogue does this for Grok: `Grok 4.7`, `Grok 4.7 High`, `Grok 4.7 Medium` and `Grok 4.7 None` all call `grok-4.7` with a different reasoning effort.

Renaming a model on the Models page updates every agent and setting that names it. A model that an agent or setting still names cannot be deleted; the refusal lists where it is used.

### Ready, configured and enabled

- On the **Providers** page, a provider card shows **Configured** when it can be used: an HTTP provider has an API key, and a CLI provider's program was found.
- On the **Models** page, a model is **Configured** when its provider has a key, when it is a CLI model, or when it is on a local server (`localhost`, `127.0.0.1`, `::1` or `0.0.0.0`) that currently lists the model. See [Local models](./local-models.md).
- A model is used only when it is **enabled** (the switch on its card).

The model pickers on the **Agents** and **System** pages offer only configured models (the Agents page only enabled ones as well). A disabled model is skipped even where it is still named.

## Which model an agent uses

Each agent has an ordered list of models. On the **Agents** page it is shown as **Models (tried in order)**; in `config.json` it is `agents.list[].models`. An agent with no list of its own uses the default list, `agents.defaults.models`, which is **Default models** on the **System** page.

- The first model in the list is the one normally used.
- The others are the **fallback chain**: when a model fails in a way another model could fix, ClawEh tries the next one in the same turn.
- **Set as default** on a model card on the Models page puts that model first in the default list.

In a chat, `/model` lists the agent's models and `/model <n>` switches the conversation to model number `n`.

From a shell, `claw model` shows the default model and the enabled models; `claw model <model_name>` makes that model the default.

### When ClawEh moves to the next model

ClawEh moves on for failures another model might not have: rate limits, an exhausted balance or rejected key, a timeout, a server error, an overloaded service, a model the endpoint no longer serves, or a request the endpoint rejected as malformed. For an error it cannot classify, ClawEh stops and reports it instead of trying the rest.

If a server answers with a short `Retry-After` (3 seconds or less), ClawEh waits and retries the same model once before moving on.

While it falls back, the person in the chat sees a short notice, for example:

```
⚠️ Claude Sonnet 4.6 error HTTP 429 (rate limited).
Trying OpenAI GPT 5.5…
```

If every model fails, the reply starts with `All models failed` and lists each model with its reason.

### Several entries with one name

In `config.json` you may give two enabled models the same `model_name`, for example the same vendor model through two providers. ClawEh then spreads requests across them in turn. The WebUI does not create such duplicates; it refuses an alias that already exists.

## Cooldowns

A model that keeps failing is taken out of rotation for a while, so each turn does not wait on it again. This is a **cooldown**. While a model is cooling down, ClawEh skips it and goes straight to the next model in the chain.

Cooldowns are tracked per provider and model id. Two aliases on the same provider and model id, such as `Grok 4.7` and `Grok 4.7 High`, share one cooldown. Different models on the same provider do not affect each other. Cooldowns are kept in memory and cleared when ClawEh restarts.

How long a model is parked depends on the HTTP status of the failure:

| Failure | Config key (minutes) | Default |
|---|---|---|
| 401, 402, 403: key rejected or out of credit | `cooldown.billing_auth_minutes` | 30, from the first failure |
| 429: rate limited | `cooldown.rate_limit_minutes` | 10 |
| 400: request rejected | `cooldown.bad_request_minutes` | 0 (never cooled) |
| Other 4xx, such as 404 | `cooldown.client_error_minutes` | 10 |
| 5xx: server error | `cooldown.server_error_minutes` | 10 |

Except for billing and key failures, the first three failures in a row park the model for 1, 3 and 5 minutes; only the fourth uses the time in the table. A success resets the count. A 413 (request too large) and a failure with no HTTP status never cause a cooldown. A `Retry-After` from the server is honoured up to 5 minutes.

In each key, 0 means the default and a negative number turns cooldown off for that kind of failure. These keys are not on a WebUI page; set them in `config.json` (the **Raw config** button on the System page) under a top-level `cooldown` block:

```json
"cooldown": {
  "rate_limit_minutes": 5
}
```

### What you see

- In the chat: `⚠️ Claude Sonnet 4.6 unavailable — rate limited (retry in 7m). Using OpenAI GPT 5.5.` Several skipped models are summed up on one line.
- Operator alerts: one when a model is parked for a key or billing failure (raised on the first such failure), and one when a model is parked after repeated failures of another kind, once the 1, 3 and 5 minute steps are used up.
- Chat commands: `/cooldowns list` shows parked models; `/cooldowns clear` clears all of them, and `/cooldowns clear <provider>/<model>` clears one. `/retry` clears all cooldowns and resends your last message.

## Context window and output length

| Setting | Where | Default |
|---|---|---|
| Context window, in tokens | **Context Window (tokens)** on the model (`context_window`); otherwise `agents.defaults.context_window` | 128000 |
| Maximum reply length, in tokens | **Max Output Tokens** on the model (`max_tokens`); otherwise **Max tokens** on the System page (`agents.defaults.max_tokens`) | 32768 |

The context window tells ClawEh how much conversation fits in one request. ClawEh compacts the conversation (summarizes older messages) when it fills a share of this window, so set it to the model's real size: too large and requests start failing as "context too large"; too small and the agent forgets sooner than it needs to.

Some endpoints call the output limit `max_completion_tokens`. ClawEh sends that name automatically for model ids containing `gpt-5`, `o1` or `glm`; for others, set **Max Tokens Field** on the model.

## Request timeout

The request timeout is how long ClawEh waits for one model call.

1. **Request Timeout (s)** on the model (`request_timeout`), if set.
2. Otherwise **Request timeout (s)** on the System page (`agents.defaults.request_timeout`), 300 seconds by default.
3. If that is 0, a built-in 120 seconds.

The CLI models are created with 3600 seconds, because a CLI runs a whole job of its own in one call.

Every turn is also bounded by **Turn timeout (s)** on the System page (`agents.defaults.turn_timeout`, 900 seconds by default), which covers all model calls and tools of the turn. It applies to CLI models too: a CLI job that should run longer than 15 minutes needs a longer turn timeout. A call that times out counts as a failure and the next model is tried.

## Models used on the side

Besides the agent's own models, two chains are set on the **System** page.

### Memory models

**Memory models** (`summarization.models`) summarize older conversation when the context fills, and consolidate the agent's long-term memory. For each agent, ClawEh tries:

1. the agent's own **Memory models (optional)** on the Agents page (`agents.list[].summarization_models`),
2. then the global **Memory models**,
3. then the agent's own model.

Use this to put summarizing on a cheaper model, or on one that will not refuse the agent's subject matter.

### Vision models

A model can see images only when its **Vision (tool-result images)** setting (`vision`) is not **Off**:

- **User message** (`user_message`): images are added as a follow-up message. Works on chat-completion models.
- **Tool response** (`tool_response`): images are attached to the tool result itself. The Responses API (`openai-responses`) takes this; most chat-completion endpoints do not, though the catalogue's Grok models on xAI use it.

When an agent's model is set to **Off** and it receives an image, ClawEh sends the image to the **Vision models** chain (`agents.defaults.vision_model`, then `agents.defaults.vision_model_fallbacks`) for a text description and gives the agent that description. With no vision model set, images are dropped for such models.

### Image model

`agents.defaults.image_model` and `image_model_fallbacks` exist in the configuration and are listed by the Check Up report, but no current ClawEh feature uses them.

## Other model settings

| Setting | Config key | Notes |
|---|---|---|
| Tools Enabled | `no_tools` (inverted) | Turn off for a model that cannot call tools. The model then gets no tools at all. |
| Strict Role Alternation | `strict_alternation` | For chat-only models that reject system and tool messages, such as Gemma. Pair it with Tools Enabled off. |
| Reasoning Effort | `reasoning_effort` | `none`, `low`, `medium` or `high`, for models that accept it. See [Cloud APIs](./cloud-apis.md#reasoning-effort). |
| Extra Request Body | `extra_body` | See [Cloud APIs](./cloud-apis.md#extra-request-body-and-drop-request-fields). |
| Drop Request Fields | `drop_params` | See [Cloud APIs](./cloud-apis.md#extra-request-body-and-drop-request-fields). |
| Workspace Path | `workspace` | CLI models only. See [CLI providers](./cli-providers.md#where-the-cli-runs). |

The model form also shows **Rate Limit (RPM)**, **Connect Mode** and **Thinking Level**. The current providers do not act on them; ClawEh logs a warning when a thinking level is set on a model that does not support it.
