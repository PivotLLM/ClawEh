# Cloud APIs

This page shows how to connect ClawEh to a vendor's API with an API key, and which provider settings matter for each vendor.

## Add a key and enable a model

The built-in catalogue already has a provider for each vendor below, with the right protocol and base URL. You only add the key.

1. Get an API key from the vendor's console.
2. In the WebUI, open **Providers**. Under **API providers**, click **Edit** on the vendor's card.
3. Paste the key into **API Key** and click **Save**. The card now shows **Configured**.
4. Open **Models**. Find the vendor's group and switch on the model you want.
5. Give the model to an agent on the **Agents** page, or click **Set as default** on its card.

If the catalogue has no model you want, add one:

1. On **Models**, click **Add Model**.
2. Fill in **Model Alias** (your name for it), **Model Identifier** (the vendor's exact model id) and **Provider**.
3. Click **Add Model**. Turn on **Default Model** first if it should become the default.

To keep a key out of `config.json`, write a reference in the `api_key` field instead of the key: `env:OPENAI_API_KEY` reads an environment variable of the service, and `file:/etc/claw/openai.key` reads a file that only its owner can read. ClawEh will not start, and the WebUI refuses the save, if the reference cannot be resolved. See [Keeping secrets out of the file](../reference/configuration.md#keeping-secrets-out-of-the-file).

## Vendors in the catalogue

| Provider | Protocol | Base URL | Catalogue models | Preset settings |
|---|---|---|---|---|
| OpenAI | `openai-chat` | `https://api.openai.com/v1` | OpenAI GPT 5.5 | Model drops `temperature` |
| Anthropic | `anthropic` | `https://api.anthropic.com/v1` | Claude Sonnet 5.5, Claude Opus 5.5, Claude Fable 5.1 | |
| Google API | `openai-chat` | `https://generativelanguage.googleapis.com/v1beta/openai` | Gemini 3.8 Flash | |
| DeepSeek | `openai-chat` | `https://api.deepseek.com/v1` | DeepSeek Flash | Require Reasoning Content |
| xAI | `openai-chat` | `https://api.x.ai/v1` | Grok 4.7, Grok 4.7 High, Grok 4.7 Medium, Grok 4.7 None | Reasoning effort per model |
| Mistral | `openai-chat` | `https://api.mistral.ai/v1` | Mistral Small | |
| Qwen | `openai-chat` | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` | Qwen Plus | |
| Moonshot | `openai-chat` | `https://api.moonshot.ai/v1` | none | |
| Groq | `openai-chat` | `https://api.groq.com/openai/v1` | Groq GPT OSS 120b | Disable Parallel Tool Calls |
| Cerebras | `openai-chat` | `https://api.cerebras.ai/v1` | none | |
| NVIDIA | `openai-chat` | `https://integrate.api.nvidia.com/v1` | none | Strict Compatibility |
| Avian | `openai-chat` | `https://api.avian.io/v1` | DeepSeek v3.2 | |
| Abliteration | `openai-chat` | `https://api.abliteration.ai/v1` | Abliteration | |

For a vendor marked "none", add the model yourself with the vendor's model id. The same goes for any vendor not listed: click **Add Provider** on the Providers page, give it a **Name**, pick the **Protocol** (usually `openai-chat`), enter the **Base URL** and **API Key**, and then add models on it.

## Protocols

| Protocol | Use it for |
|---|---|
| `openai-chat` | Any endpoint that speaks OpenAI's Chat Completions API. Most vendors above, OpenRouter, and local servers. |
| `openai-responses` | Endpoints that speak OpenAI's Responses API, such as the OpenRouter Responses and Bedrock Responses providers. Use it for a model that should see images returned by tools (vision **Tool response**). |
| `azure` | Azure OpenAI. The base URL is your resource endpoint and the **Model Identifier** is your deployment name. |
| `anthropic` | Anthropic's Messages API. `anthropic-messages` is the same protocol under another name. |

The HTTP protocols require a base URL. The `anthropic` protocol does not use **HTTP Proxy**, **Reasoning Effort**, **Extra Request Body** or **Drop Request Fields**.

On `openai-chat` providers other than OpenRouter, ClawEh removes one leading `litellm/`, `moonshot/`, `nvidia/`, `groq/`, `ollama/`, `deepseek/`, `google/`, `openrouter/` or `mistral/` from the model id before sending it. Other prefixes, such as the `openai/` in `openai/gpt-oss-120b` on Groq, are sent as written.

## Provider settings

These are on the provider's edit form and apply to every model on it.

| Setting | Config key | Turn it on when |
|---|---|---|
| Strict Compatibility | `strict_compat` | The endpoint rejects fields outside the basic OpenAI schema, such as `reasoning_content`. ClawEh strips them from the conversation it sends. `openai-chat` only. |
| Require Reasoning Content | `require_reasoning_content` | The endpoint rejects a conversation whose earlier replies have no reasoning, as DeepSeek V4 does in thinking mode when tools are sent. ClawEh fills in a placeholder where reasoning is missing and leaves real reasoning alone. This is the opposite of Strict Compatibility; never turn on both. |
| Disable Parallel Tool Calls | `no_parallel_tool_calls` | The model misbehaves when it calls several tools at once. ClawEh sends `parallel_tool_calls: false` with each request that offers tools. |
| JSON Response Format | `response_format_json` | Lets ClawEh ask an `openai-responses` endpoint for JSON output when it summarizes. `openai-chat` providers are asked anyway. |
| HTTP Proxy | `proxy` | Requests to this provider must go through a proxy, for example `http://127.0.0.1:7890`. |

Require Reasoning Content matters most when an agent switches models: after a turn on a model that records no reasoning (any CLI model, for example), DeepSeek would otherwise reject every following turn.

## Model settings for HTTP providers

### Temperature

ClawEh sends an HTTP model the agent's `temperature`, else **Default temperature** on the System page (`agents.defaults.temperature`). When neither is set it sends no temperature, and the model uses its own default. Some reasoning models reject any temperature. For those, list `temperature` in the model's **Drop Request Fields**, as the catalogue does for OpenAI GPT 5.5.

### Reasoning effort

**Reasoning Effort** (`reasoning_effort`) asks models that support it how hard to think: `none`, `low`, `medium` or `high`. Leave it unset to send nothing. `none` is sent as written, to turn reasoning off on models that reason by default.

- On `openai-chat` it is sent as the `reasoning_effort` field. xAI's Grok models take it; the catalogue's four Grok entries differ only in this setting.
- On `openai-responses` it is sent as `reasoning.effort`. With `low`, `medium` or `high`, ClawEh leaves out the temperature.
- Other values are refused when the configuration is saved or loaded.

### Extra Request Body and Drop Request Fields

**Extra Request Body** (`extra_body`) is a JSON object merged into every request, for vendor options ClawEh does not offer as settings. For example, on xAI:

```json
{
  "search_parameters": { "mode": "on" }
}
```

ClawEh refuses keys it sets itself: `model`, `messages`, `stream`, `tools`, `tool_choice`, `parallel_tool_calls`, `reasoning_effort`, `temperature`, `max_tokens`, `max_completion_tokens`, `top_p` and `n`. Use the matching setting for those.

**Drop Request Fields** (`drop_params`) lists top-level request fields to remove before sending, comma-separated in the WebUI, for example `temperature`. It is applied after everything else, so it always wins. It is a plain filter: dropping `messages` or `model` breaks every request.

Both apply to `openai-chat` and `openai-responses` models. For their use with OpenRouter, see [OpenRouter](./openrouter.md).

## Vendor notes

- **OpenAI**: the catalogue provider uses Chat Completions. To use the Responses API instead, add a provider with protocol `openai-responses` and the same base URL.
- **DeepSeek**: keep **Require Reasoning Content** on. The catalogue model sets a context window of 900000 tokens; adjust it if your model differs.
- **Groq**: the provider ships with **Disable Parallel Tool Calls** on.
- **NVIDIA**: the provider ships with **Strict Compatibility** on.
- **xAI**: the Grok models are set to **Tool response** vision and a 500000-token context window.

See [Concepts](./concepts.md) for context windows, timeouts and what happens when a model fails.
