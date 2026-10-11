# Amazon Bedrock

This page shows how to use models hosted on Amazon Bedrock through its OpenAI-compatible endpoints.

ClawEh reaches Bedrock the same way it reaches other cloud APIs: over HTTPS with an API key sent as a bearer token. It does not sign requests with AWS access keys or an IAM role, so you need an **Amazon Bedrock API key**, and the models you want must be available to your account in the region you use.

## The two Bedrock providers

| Provider | Protocol | Base URL |
|---|---|---|
| Bedrock Chat | `openai-chat` | `https://bedrock-mantle.us-east-1.api.aws/v1` |
| Bedrock Responses | `openai-responses` | `https://bedrock-mantle.us-east-1.api.aws/openai/v1` |

Both point at the `us-east-1` region. To use another region, edit the provider and change the region in **Base URL**.

Catalogue models, all disabled:

| Model | Model Identifier | Provider | Preset settings |
|---|---|---|---|
| Bedrock Opus 5.5 | `anthropic.claude-opus-5-5` | Bedrock Chat | |
| Bedrock Sonnet 5.5 | `anthropic.claude-sonnet-5-5` | Bedrock Chat | |
| Bedrock DeepSeek 3 | `deepseek.v3.2` | Bedrock Chat | |
| Bedrock Nova 2 Lite | `amazon.nova-2-lite-v1:0` | Bedrock Chat | |
| Bedrock Gemma 3 | `google.gemma-3-27b-it` | Bedrock Chat | Max Output Tokens 5000, Strict Role Alternation |
| Bedrock GPT 5.5 | `openai.gpt-5.5` | Bedrock Responses | Drops `temperature` |
| Bedrock GPT 6 Astra | `openai.gpt-6-astra` | Bedrock Responses | Context window 250000 |

## Set up

1. Create an Amazon Bedrock API key in your AWS account, and make sure the models you want are enabled for it in your region.
2. In **Providers**, click **Edit** on **Bedrock Chat**, paste the key into **API Key**, check **Base URL** for your region, and click **Save**. Do the same on **Bedrock Responses** if you use its models.
3. In **Models**, switch on the models you want.

To add another Bedrock model, click **Add Model** on the Models page and use Bedrock's model id as **Model Identifier**. Put OpenAI models on **Bedrock Responses** and the others on **Bedrock Chat**, following the catalogue.

## Models that need special settings

- **Gemma** accepts only alternating user and assistant messages, with no system or tool messages. Its catalogue entry has **Strict Role Alternation** on, which folds the system prompt into the first user message and turns tool results into user messages. It cannot call tools, so also turn **Tools Enabled** off for it.
- **Temperature**: the catalogue's Bedrock GPT 5.5 lists `temperature` under **Drop Request Fields** because the model refuses it. If another model's requests are rejected over the temperature, do the same.

See [Cloud APIs](./cloud-apis.md#model-settings-for-http-providers) for these settings in general.
