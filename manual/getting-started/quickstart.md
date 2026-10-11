# Quick start

This page takes a fresh install to a first chat: sign in, run the setup wizard, grant your agent its tools, and send a message.

It assumes ClawEh is installed and running. If not, see [Installing ClawEh](./install.md).

## 1. Sign in

1. Open the address the installer printed, for example `http://127.0.0.1:18790/` on the same machine. `claw status` prints it again.
2. Sign in with the admin account you created during the install.

If the page says "No admin account", run `claw admin` on the server and reload the page. See [Signing in](../admin/login.md).

## 2. Run the setup wizard

On a fresh install the setup wizard opens by itself. It has six steps:

1. **Welcome** — what the wizard will set up.
2. **Network** — whether other machines may reach the WebUI ([Network access](../admin/network.md)). Leave **Enable network access** off unless you need it. You can change it later on the **Network** page.
3. **Provider** — choose a provider, paste its API key, and click **Test key** (see [Providers and models](../providers/index.md)). CLIs installed on this machine (Claude Code, Codex, Antigravity, Cursor) are listed too and need no key.
4. **Model** — choose the model to use by default.
5. **Agent** — give your first agent a name.
6. **Review** — check the summary and click **Finish setup**.

Your agent is now the [default agent](../agents/creating.md#the-default-agent-and-default-models), using the model you picked.

## 3. Give your agent tools

A new agent has no tools and no skills. It can talk, but it cannot read files, search the web or do anything else until you grant it.

1. Open **Agents** in the sidebar and select your agent in the list on the left.
2. Under **Tools**, find **Internal tools: No tools** and click **Default tools**. This adds the standard set. You can untick tools you do not want.
3. Under **Skills**, click **All skills** if you want the agent to use the installed skills.

Changes on the Agents page are saved as you make them.

Some capabilities have their own switch on the same card, each off by default:

- **Allow shell commands** lets the agent run commands on this machine.
- **Maestro**, **Fusion (REST-API tools)** and **Allow forum** add their tool suites.
- **Cognitive memory** is on by default; turn it off for an agent that should keep no memory.

Grant only what the agent needs. See [Tools and skills](../agents/tools-and-skills.md) for every switch, and [Maestro](../maestro.md), [MCPFusion](../mcpfusion.md) and [Forums](../forums/index.md) for the suites.

## 4. Chat

Open **Chat** at the top of the sidebar and type a message. The WebUI chat goes to the default agent, unless a binding on the **Bindings** page sends it to another one (see [WebUI chat](../channels/webui-chat.md) and [Routing and bindings](../channels/routing.md)).

The agent has one conversation. Messages from other channels, such as Telegram, join the same conversation (see [Conversation and memory](../agents/memory.md)).

## 5. Check the install

Open **Check Up** in the sidebar. It lists the security-relevant settings of this install and marks the ones that need attention. A row **No tools (…)** means an agent still has no tools. See [Check Up and the full report](../admin/checkup.md).

## Next steps

- Connect a messaging app such as Telegram or Slack. See [Channels](../channels/index.md).
- Add more providers and models, and set the order an agent tries them in. See [Providers and models](../providers/index.md).
- Create more agents for separate conversations or purposes. See [Agents](../agents/index.md).
- Reach the WebUI from other machines. See [Network access](../admin/network.md).
