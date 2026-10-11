# Local models

This page shows how to use models you run yourself, with Ollama, vLLM or any other server that speaks the OpenAI Chat Completions API.

ClawEh talks to a local server like any other `openai-chat` endpoint. No API key is needed when the server does not ask for one.

## The catalogue entries

| Provider | Base URL | Catalogue model | Model Identifier |
|---|---|---|---|
| Ollama | `http://localhost:11434/v1` | Llama 3 | `llama3` |
| vLLM Local | `http://localhost:8000/v1` | Local Model | `custom-model` (a placeholder) |

## Ollama

1. Install Ollama and pull a model, for example `ollama pull llama3`.
2. Find the exact id Ollama serves it under: `curl http://localhost:11434/v1/models`. Ollama lists models with their tag, such as `llama3:latest`.
3. In **Models**, click **Edit model** on **Llama 3** (or click **Add Model** and pick the **Ollama** provider), and set **Model ID** to that exact id.
4. Set **Context Window (tokens)** to the context size your Ollama model runs with.
5. Switch the model on.

## vLLM and other OpenAI-compatible servers

1. Start the server with an OpenAI-compatible API.
2. Edit the **vLLM Local** provider (or add a provider with protocol `openai-chat`) and set **Base URL** to the server's `/v1` address. If the server was started with an API key, enter it under **API Key**.
3. Edit the **Local Model** entry and replace `custom-model` with the model name the server serves (as listed at `<base URL>/models`).
4. Set **Context Window (tokens)** and switch the model on.

## How ClawEh checks a local model

A model whose provider's base URL is on this machine (`localhost`, `127.0.0.1`, `::1` or `0.0.0.0`) needs no key. Instead, each time the Models page loads, ClawEh asks the server for `<base URL>/models` and shows the model as **Configured** only when the server answers within a second and lists the **Model Identifier** (letter case ignored). If the server is stopped or the id is spelled differently, the model shows **Not configured** and is not offered in the model pickers on the Agents and System pages.

A server on another machine, for example `http://192.168.1.20:11434/v1`, is treated like a cloud API: the model shows as **Configured** only when the provider has an API key. If the server needs none, enter any placeholder value under **API Key**.

## Tips for small local models

- Many local models handle tool calls poorly. If a model answers with tool-call text instead of using the tools, turn **Tools Enabled** off for it, or keep it for memory summarizing only (**Memory models** on the System page).
- Models that reject system messages, or that need user and assistant messages to alternate, need **Strict Role Alternation** on and **Tools Enabled** off.
- A local model is a good last entry in an agent's model list: when the cloud models are rate limited or out of credit, the agent still answers. See [Concepts](./concepts.md#which-model-an-agent-uses).
