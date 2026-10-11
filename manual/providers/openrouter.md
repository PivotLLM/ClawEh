# OpenRouter

This page explains the four OpenRouter providers in the catalogue, which one to use for which model, and why OpenRouter sometimes refuses a model.

OpenRouter reaches many vendors' models through one API key and one bill. ClawEh ships four providers that all point at `https://openrouter.ai/api/v1` with different settings. You need the key on each one you use.

## Set up

1. Create an API key in your OpenRouter account.
2. In **Providers**, under **API providers**, click **Edit** on each OpenRouter provider you plan to use, paste the key into **API Key** and click **Save**. To keep one copy of the key, write `env:OPENROUTER_API_KEY` (or any variable name you choose) in each, and set that variable in the service's environment (see [Keeping secrets out of the file](../reference/configuration.md#keeping-secrets-out-of-the-file)).
3. In **Models**, switch on the models you want. They are grouped by provider.

## Which provider

| Provider | Protocol | Setting | Use it for |
|---|---|---|---|
| OpenRouter Chat | `openai-chat` | | Most models: Llama, Kimi, Gemini, Claude through OpenRouter. |
| OpenRouter Responses | `openai-responses` | | OpenAI models and others offered on OpenRouter's Responses API, especially when the model should see images returned by tools (vision **Tool response**). |
| OpenRouter Strict | `openai-chat` | Strict Compatibility | Models whose upstream rejects fields outside the basic OpenAI schema. The catalogue uses it for OpenRouter GPT 5.5. |
| OpenRouter Chat DeepSeek | `openai-chat` | Require Reasoning Content | DeepSeek models in thinking mode, which reject a conversation whose earlier replies carry no reasoning. |

A model belongs to one provider, so to try a model on another provider, add a second model with the same **Model Identifier** on the other provider and a different alias.

### DeepSeek through OpenRouter

DeepSeek's thinking models, when tools are offered, require every earlier reply in the conversation to carry its reasoning. Replies that came from another model, such as a CLI model or a fallback, have none, and DeepSeek then rejects every following turn. **Require Reasoning Content** (`require_reasoning_content`) fills in a placeholder where reasoning is missing. Put DeepSeek models on **OpenRouter Chat DeepSeek**, which has it on, and not on **OpenRouter Strict**, which does the opposite.

## "Latest" model names

Several catalogue models use OpenRouter names that follow a vendor's newest release rather than a fixed version, for example:

| Model | Model Identifier |
|---|---|
| OpenRouter Fable Latest | `anthropic/claude-fable-latest` |
| OpenRouter Kimi Latest | `moonshotai/kimi-latest` |
| OpenRouter Gemini Flash Latest | `google/gemini-flash-latest` |
| OpenRouter GPT Terra Latest | `openai/gpt-terra-latest` |
| OpenRouter GLM Latest | `z-ai/glm-latest` |
| OpenRouter DeepSeek Flash Latest | `~deepseek/deepseek-flash-latest` |
| OpenRouter DeepSeek Pro Latest | `~deepseek/deepseek-pro-latest` |

OpenRouter decides which model such a name points to, so the model behind an agent can change without any change on your side. Copy these identifiers exactly, including a leading `~`. When you need a fixed model, use OpenRouter's versioned id instead. The **Context Window (tokens)** set on these models is ClawEh's assumption; correct it if the model behind the name changes size.

## Why OpenRouter refuses a model

When a request offers tools, ClawEh adds `"provider": {"require_parameters": true}` to requests sent to OpenRouter on the `openai-chat` protocol. This tells OpenRouter to route only to upstream hosts that support every parameter in the request. Without it, OpenRouter may pick a host that ignores the tools and answers in plain text.

The catch: if the request carries a parameter that no host of that model advertises, OpenRouter finds no host and answers HTTP 404 "No endpoints found…". ClawEh reports the model as `model not available`, puts it in cooldown and moves to the next model in the chain.

The two usual causes:

1. **Temperature.** ClawEh sends a temperature when the agent or **Default temperature** sets one. Many reasoning models do not accept it. Fix: add `temperature` to the model's **Drop Request Fields** (`drop_params`). The catalogue does this for OpenRouter GPT 5.5.
2. **Parallel tool calls.** **Disable Parallel Tool Calls** (`no_parallel_tool_calls`) on the provider sends `parallel_tool_calls: false`. If no host of the model supports that parameter, the model is refused. Fix: turn the setting off for the provider, or move the model to a provider without it.

You can also list `provider` in **Drop Request Fields** to remove the routing hint altogether. The model is then no longer refused, but OpenRouter may again route it to a host that ignores tools.

## Pinning an upstream host

OpenRouter lets a request choose or exclude upstream hosts with its `provider` object. You can send one through the model's **Extra Request Body** (`extra_body`), for example:

```json
{
  "provider": {
    "order": ["deepinfra"],
    "allow_fallbacks": false
  }
}
```

This works on **OpenRouter Responses** models, and on `openai-chat` models with **Tools Enabled** off. On an `openai-chat` model that is offered tools, ClawEh already sends its own `provider` object (see above), so your `provider` entry is skipped and a warning naming the key is written to the log.

## Related

- [Cloud APIs](./cloud-apis.md#extra-request-body-and-drop-request-fields) for Extra Request Body and Drop Request Fields in general.
- [Concepts](./concepts.md#cooldowns) for what a cooldown does and how to clear one.
