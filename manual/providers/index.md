# Providers and models

This section explains how ClawEh reaches the language models your agents use, and how to set each kind up.

An agent never talks to a vendor directly. It names one or more **models**, and each model is reached through a **provider**: a named endpoint with a protocol, an address and, for cloud services, an API key. ClawEh ships with a catalogue of providers and models, all switched off, so most setups come down to adding a key and turning a model on.

Both lists live in the WebUI under **Models** in the sidebar:

- **Providers** lists the local CLI agents (Claude CLI, Codex CLI, Antigravity CLI, Cursor CLI), each with an on/off switch, and the API providers, each marked **Configured** once it has a key.
- **Models** lists every model, grouped by provider, with a switch to enable or disable each one.

## Pages in this section

| Page | Read it when |
|---|---|
| [Concepts](./concepts.md) | You want to understand providers, models, fallback chains, cooldowns, timeouts and the side models used for memory and images. Start here. |
| [Cloud APIs](./cloud-apis.md) | You have an API key from OpenAI, Anthropic, Google, DeepSeek, xAI, Mistral, Qwen, Moonshot, Groq, Cerebras, NVIDIA, Avian or Abliteration. |
| [OpenRouter](./openrouter.md) | You use OpenRouter, which reaches many vendors through one key. |
| [Amazon Bedrock](./bedrock.md) | You use models hosted on Amazon Bedrock. |
| [Local models](./local-models.md) | You run models yourself with Ollama, vLLM or another OpenAI-compatible server. |
| [CLI providers](./cli-providers.md) | You want an agent to run on Claude Code, Codex, Antigravity or Cursor installed on the same machine. |

## The short version

1. Open **Providers**, click the edit button on the provider you want, enter its **API Key** and save. For a CLI agent, switch it on in **Local CLI agents** instead.
2. Open **Models** and switch on the model you want.
3. Give the model to an agent: on the **Agents** page, add it under **Models (tried in order)**, or click **Set as default** on the model card to make it the default for every agent that has no list of its own.

::: tip
The setup wizard that opens on a fresh install does these steps for you for the first provider and model. Use this section for everything after that.
:::
