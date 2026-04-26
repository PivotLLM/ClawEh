# Migration Guide: From `providers` to `model_list`

This guide explains how to migrate from the legacy `providers` configuration to the new `model_list` format.

## Why Migrate?

The new `model_list` configuration offers several advantages:

- **Zero-code provider addition**: Add OpenAI-compatible providers with configuration only
- **Load balancing**: Configure multiple endpoints for the same model
- **Protocol-based routing**: Use prefixes like `openai/`, `anthropic/`, etc.
- **Cleaner configuration**: Model-centric instead of vendor-centric

## Timeline

| Version | Status |
|---------|--------|
| v1.x | `model_list` introduced, `providers` deprecated but functional |
| v1.x+1 | Prominent deprecation warnings, migration tool available |
| v2.0 | `providers` configuration removed |

## Before and After

### Before: Legacy `providers` Configuration

```json
{
  "providers": {
    "openai": {
      "api_key": "sk-your-openai-key",
      "api_base": "https://api.openai.com/v1"
    },
    "anthropic": {
      "api_key": "sk-ant-your-key"
    },
    "deepseek": {
      "api_key": "sk-your-deepseek-key"
    }
  },
  "agents": {
    "defaults": {
      "provider": "openai",
      "model": "gpt-5.4"
    }
  }
}
```

### After: New `model_list` Configuration

```json
{
  "model_list": [
    {
      "model_name": "gpt4",
      "model": "openai/gpt-5.4",
      "api_key": "sk-your-openai-key",
      "api_base": "https://api.openai.com/v1"
    },
    {
      "model_name": "claude-sonnet-4.6",
      "model": "anthropic/claude-sonnet-4.6",
      "api_key": "sk-ant-your-key"
    },
    {
      "model_name": "deepseek",
      "model": "deepseek/deepseek-chat",
      "api_key": "sk-your-deepseek-key"
    }
  ],
  "agents": {
    "defaults": {
      "model": "gpt4"
    }
  }
}
```

## Protocol Prefixes

The `model` field uses a protocol prefix format: `[protocol/]model-identifier`

| Prefix | Description | Example |
|--------|-------------|---------|
| `openai/` | OpenAI API (default) | `openai/gpt-5.4` |
| `anthropic/` | Anthropic API | `anthropic/claude-sonnet-4.6` |
| `anthropic-messages/` | Anthropic Messages API (native format) | `anthropic-messages/claude-sonnet-4.6` |
| `azure/` or `azure-openai/` | Azure OpenAI (deployment-based URLs) | `azure/my-gpt5-deployment` |
| `bedrock/` | AWS Bedrock | `bedrock/us.anthropic.claude-sonnet-4-20250514-v1:0` |
| `antigravity/` | Google Cloud Code Assist (OAuth) | `antigravity/gemini-2.0-flash` |
| `gemini/` | Google Gemini API | `gemini/gemini-2.0-flash-exp` |
| `claude-cli/` | Claude CLI subprocess (local) | `claude-cli/claude-cli` |
| `codex-cli/` | Codex CLI subprocess (local) | `codex-cli/codex-cli` |
| `gemini-cli/` | Gemini CLI subprocess (local) | `gemini-cli/gemini-cli` |
| `github-copilot/` | GitHub Copilot | `github-copilot/gpt-4o` |
| `litellm/` | LiteLLM proxy | `litellm/gpt-5.4` |
| `openrouter/` | OpenRouter | `openrouter/anthropic/claude-sonnet-4.6` |
| `groq/` | Groq API | `groq/llama-3.3-70b-versatile` |
| `deepseek/` | DeepSeek API | `deepseek/deepseek-chat` |
| `cerebras/` | Cerebras API | `cerebras/llama-3.3-70b` |
| `mistral/` | Mistral AI | `mistral/mistral-small-latest` |
| `qwen/` | Alibaba Qwen | `qwen/qwen-plus` |
| `nvidia/` | NVIDIA NIM | `nvidia/nemotron-4-340b-instruct` |
| `ollama/` | Ollama (local) | `ollama/llama3` |
| `vllm/` | vLLM (local) | `vllm/my-model` |
| `moonshot/` | Moonshot AI | `moonshot/moonshot-v1-8k` |
| `avian/` | Avian | `avian/deepseek/deepseek-v3.2` |
| `shengsuanyun/` | ShengSuanYun | `shengsuanyun/deepseek-v3` |
| `volcengine/` | Volcengine | `volcengine/doubao-pro-32k` |
| `zhipu/` | Zhipu AI | `zhipu/glm-4` |

**Note**: If no prefix is specified, `openai/` is used as the default.

## ModelConfig Fields

| Field | Required | Description |
|-------|----------|-------------|
| `model_name` | Yes | User-facing alias for the model |
| `model` | Yes | Protocol and model identifier (e.g., `openai/gpt-5.4`) |
| `api_base` | No | API endpoint URL (see per-protocol notes below) |
| `api_key` | No* | API authentication key (see per-protocol notes below) |
| `proxy` | No | HTTP proxy URL |
| `auth_method` | No | Authentication method: `oauth`, `token` |
| `connect_mode` | No | Connection mode for CLI providers: `stdio`, `grpc` |
| `workspace` | No | Working directory for CLI-based providers (`claude-cli`, `codex-cli`, `gemini-cli`) |
| `command` | No | Override the binary path for CLI-based providers (e.g., `/home/user/.local/bin/claude`) |
| `rpm` | No | Requests per minute limit |
| `max_tokens_field` | No | Override the max tokens field name (e.g., `max_completion_tokens` for o1/GLM models) |
| `request_timeout` | No | HTTP request timeout in seconds; `<=0` uses default `120s` |
| `thinking_level` | No | Extended thinking budget: `off`, `low`, `medium`, `high`, `xhigh`, `adaptive` |
| `strict_compat` | No | Strip non-standard fields for strict OpenAI-compatible endpoints |
| `no_tools` | No | When `true`, no tools are passed to this model (for models that don't support tool calling) |
| `enabled` | No | Set to `false` to disable the model without removing it |
| `extra_args` | No | Additional CLI arguments for subprocess-based providers |

*`api_key` is required for HTTP-based protocols unless `api_base` points to a local server.

### Per-Protocol Notes

#### `bedrock/`
- **`api_base`**: AWS region name (e.g., `us-east-1`) — the SDK resolves the endpoint automatically.
  Alternatively, a full endpoint URL: `https://bedrock-runtime.us-east-1.amazonaws.com`
- **`api_key`**: One of:
  - Omit to use the AWS default credential chain (env vars `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`, `~/.aws/credentials`, IAM instance roles, etc.)
  - `ACCESS_KEY_ID:SECRET_ACCESS_KEY` (colon-separated static credentials)
  - `ACCESS_KEY_ID:SECRET_ACCESS_KEY:SESSION_TOKEN` (with session token)
  - A Bedrock API key starting with `bak-` (bearer token auth)

#### `azure/` / `azure-openai/`
- **`api_base`**: Required. Your Azure resource endpoint, e.g., `https://your-resource.openai.azure.com`
- **`api_key`**: Required. Your Azure API key.
- The model path after `azure/` is your deployment name, e.g., `azure/my-gpt5-deployment`

#### `claude-cli/`, `codex-cli/`, `gemini-cli/`
- **`workspace`**: Directory where the CLI subprocess runs. Defaults to `.`
- **`command`**: Absolute path to the CLI binary. Omit to resolve via `PATH` (e.g., `/home/user/.local/bin/claude`). Useful in systemd services where the user's PATH is not inherited.
- **`request_timeout`**: Recommended to set high (e.g., `3600`) as these can run long tasks.
- No `api_key` or `api_base` needed — credentials are managed by the underlying CLI tool.

#### `github-copilot/`
- **`api_base`**: Address of the local Copilot Language Server. Defaults to `localhost:4321`
- **`connect_mode`**: `grpc` (default) or `stdio`

## Load Balancing

Configure multiple endpoints for the same model to distribute load:

```json
{
  "model_list": [
    {
      "model_name": "gpt4",
      "model": "openai/gpt-5.4",
      "api_key": "sk-key1",
      "api_base": "https://api1.example.com/v1"
    },
    {
      "model_name": "gpt4",
      "model": "openai/gpt-5.4",
      "api_key": "sk-key2",
      "api_base": "https://api2.example.com/v1"
    },
    {
      "model_name": "gpt4",
      "model": "openai/gpt-5.4",
      "api_key": "sk-key3",
      "api_base": "https://api3.example.com/v1"
    }
  ]
}
```

When you request model `gpt4`, requests will be distributed across all three endpoints using round-robin selection.

## Adding a New OpenAI-Compatible Provider

With `model_list`, adding a new provider requires zero code changes:

```json
{
  "model_list": [
    {
      "model_name": "my-custom-llm",
      "model": "openai/my-model-v1",
      "api_key": "your-api-key",
      "api_base": "https://api.your-provider.com/v1"
    }
  ]
}
```

Just specify `openai/` as the protocol (or omit it for the default), and provide your provider's API base URL.

## Backward Compatibility

During the migration period, your existing `providers` configuration will continue to work:

1. If `model_list` is empty and `providers` has data, the system auto-converts internally
2. A deprecation warning is logged: `"providers config is deprecated, please migrate to model_list"`
3. All existing functionality remains unchanged

## Migration Checklist

- [ ] Identify all providers you're currently using
- [ ] Create `model_list` entries for each provider
- [ ] Use appropriate protocol prefixes
- [ ] Update `agents.defaults.model` to reference the new `model_name`
- [ ] Test that all models work correctly
- [ ] Remove or comment out the old `providers` section

## Troubleshooting

### Model not found error

```
model "xxx" not found in model_list or providers
```

**Solution**: Ensure the `model_name` in `model_list` matches the value in `agents.defaults.model`.

### Unknown protocol error

```
unknown protocol "xxx" in model "xxx/model-name"
```

**Solution**: Use a supported protocol prefix. See the [Protocol Prefixes](#protocol-prefixes) table above.

### Missing API key error

```
api_key or api_base is required for HTTP-based protocol "xxx"
```

**Solution**: Provide `api_key` and/or `api_base` for HTTP-based providers.

## Need Help?

- [GitHub Issues](https://github.com/PivotLLM/ClawEh/issues)
