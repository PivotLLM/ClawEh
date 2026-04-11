# @Agent Mention Routing Design

## Goal

Allow a single channel (Telegram group, Slack workspace, Discord server, etc.) to reach multiple named agents using a trigger-prefix syntax, while preserving the existing single-agent routing for all channels that don't opt in.

Example: a Telegram group shared by Alice and Bob agents, where:
- Unaddressed messages → alice (default)
- `@alice status report please` → alice receives `status report please`
- `@bob run the build` → bob receives `run the build`

---

## Current Routing Architecture

Routing today is **purely structural** — no message content is inspected:

```
Message → BaseChannel.HandleMessage → PublishInbound (bus)
                ↓
   Loop.resolveMessageRoute → RouteResolver.ResolveRoute
                ↓
   7-level priority cascade (peer > parent_peer > guild > team > account > channel_wildcard > default)
                ↓
   AgentInstance selected → session key derived → LLM call
```

Key files:
- `pkg/routing/route.go:42` — 7-level priority cascade
- `pkg/agent/loop.go:793` — route resolution in message loop
- `pkg/config/config.go:185` — `AgentBinding` / `BindingMatch` types
- `pkg/channels/base.go:124` — `ShouldRespondInGroup` (group message gating)
- `pkg/channels/base.go:232` — `HandleMessage` (where metadata is assembled)

`ShouldRespondInGroup` is only called for group/channel messages. DMs always pass through unconditionally.

---

## Use Cases

All scenarios reduce to two cases:

### Use Case 1 — Exclusive (current behaviour, unchanged)

One binding, one agent, no multi-agent mentions. Every message that passes the group trigger goes to that agent.

```json
{"agent_id": "alice", "match": {"channel": "telegram", "peer": {"kind": "group", "id": "-100123"}}}
```

### Use Case 2 — Multi-agent

One binding with a default `agent_id` and an `agent_mentions` whitelist. The trigger prefix + agent name routes to the named agent; everything else goes to the default. Works identically for groups and DMs — the only difference is that `group_trigger` / `mention_only` applies to groups only (DMs always pass through).

```json
{
  "agent_id": "alice",
  "agent_mentions": ["alice", "bob"],
  "match": {"channel": "telegram", "peer": {"kind": "group", "id": "-100123"}}
}
```

| `group_trigger` setting | Unaddressed message | `@alice msg` | `@bob msg` | `@carol msg` |
|---|---|---|---|---|
| not set / `mention_only: false` | → alice | → alice, receives `msg` | → bob, receives `msg` | → alice (carol not listed) |
| `mention_only: true` | dropped | → alice, receives `msg` | → bob, receives `msg` | dropped |

For DMs and the channel-level fallback (no peer specified), a broad binding catches all messages on that channel that don't match a more specific peer binding, including direct messages:

```json
{
  "agent_id": "alice",
  "agent_mentions": ["alice", "bob"],
  "match": {"channel": "telegram"}
}
```

DMs always reach alice (unaddressed) or the mentioned agent — `mention_only` has no effect on DMs.

**Agents must be listed in `agent_mentions` to be addressable.** Alice should be included in her own list for explicitness. Agents not listed receive no mentions for that binding, even if they exist in the system.

---

## Trigger Prefix

A configurable list of single characters at the global or channel level. The trigger fires when a message begins with one of the listed characters immediately followed by a valid agent name and a space:

```
<trigger_char><agent_name><space><message>
```

**Default trigger characters:** `["@", "/", "."]`

- `@alice do X` → routes to alice with content `do X`
- `.alice do X` → routes to alice with content `do X`
- `/alice do X` → routes to alice with content `do X`

**Rules:**
- Match is case-insensitive (`@Alice` == `@alice`)
- Agent name must be followed by a space — bare `@alice` (nothing after) does not trigger routing (nothing to send)
- Only the **first** trigger match is used for routing; subsequent `@names` in the message are passed as content unchanged (`@alice @bob said he can't do it` → routes to alice, bob is data)
- The trigger character + agent name + space are stripped before delivery; alice receives `do X`, not `@alice do X`
- An agent name not in the binding's `agent_mentions` list does not trigger routing, even if it matches a valid agent ID

**Platform notes:**

| Platform | `@` conflict | `/` conflict | `.` conflict |
|----------|-------------|-------------|-------------|
| Telegram | `@username` is a native entity mention — no technical conflict if agent names aren't registered Telegram usernames, but semantically familiar | `/command` is the Telegram bot command format; already handled — unknown `/commands` fall through to the LLM, so `/alice` would be intercepted by mention routing first | None |
| Slack | `<@USERID>` is native; plain `@name` in text is unambiguous | Low | None |
| Discord | `<@USERID>` is native; plain `@name` in text is unambiguous | Low | None |
| CLI / web | None | None | None |

**`>` is excluded from defaults** — it is blockquote syntax on Slack, Discord, and Telegram. Messages like `> some quoted text` would false-trigger if the quoted text started with an agent name.

The trigger list is configurable so operators can remove `/` (if it conflicts with existing command handling on a given platform) or add alternatives.

---

## Config Changes

### `AgentBinding` — add `AgentMentions`

```go
// pkg/config/config.go
type AgentBinding struct {
    AgentID       string       `json:"agent_id"`
    AgentMentions []string     `json:"agent_mentions,omitempty"`
    Match         BindingMatch `json:"match"`
}
```

`AgentMentions` is the explicit whitelist of agent IDs reachable via trigger prefix in this binding. If empty, mention routing is disabled for this binding — existing behaviour is fully preserved.

### Global trigger prefix config

```go
// pkg/config/config.go — top-level or within a new AgentMentionConfig block
type AgentMentionConfig struct {
    Triggers []string `json:"triggers,omitempty"` // default: ["@", "/", "."]
}
```

---

## Implementation Design

### 1. Mention extraction helper (`pkg/channels/base.go`)

A shared helper, called in `BaseChannel.HandleMessage` before publishing to the bus:

```go
// ExtractAgentMention checks whether text begins with a known trigger character
// followed by a valid agent name and a space. Returns the matched agent name
// (normalized, lowercase) and the stripped content, or ("", original) if no match.
func ExtractAgentMention(text string, triggers []string, knownAgents []string) (agentName string, stripped string)
```

- Checks each trigger character in order
- If trigger + agent name (case-insensitive match against `knownAgents`) + space found at position 0: return normalized name + remainder
- Otherwise return `("", text)`
- Injects `mentioned_agent` into the message metadata map
- Replaces `Content.Text` with the stripped content

### 2. `RouteInput` — add `MentionedAgent`

```go
// pkg/routing/route.go
type RouteInput struct {
    Channel       string
    AccountID     string
    Peer          *RoutePeer
    ParentPeer    *RoutePeer
    GuildID       string
    TeamID        string
    MentionedAgent string  // normalized agent ID extracted from content, or ""
}
```

### 3. Routing logic — mention check inside binding evaluation

Rather than adding new priority levels, the mention check is applied **within** the existing peer/account/channel binding matches. When a binding is the candidate match, if `MentionedAgent` is set and the binding has `AgentMentions`, check the whitelist:

- If `MentionedAgent` is in `AgentMentions` → override `agent_id` with `MentionedAgent`
- If `MentionedAgent` is set but NOT in `AgentMentions` → still match the binding, route to the binding's `agent_id` (treat the unrecognised mention as plain text)
- If `AgentMentions` is empty → ignore `MentionedAgent`, route to `agent_id` as today

This avoids adding new priority levels and keeps the cascade unchanged. The mention only affects *which agent* within a matched binding, not *which binding* wins.

### 4. Session key

No changes needed. Each agent already gets a distinct session key (`agent:{agentID}:{channel}:{kind}:{peerID}`), so Alice and Bob each maintain their own independent conversation history within the same group.

---

## Interaction with `group_trigger` / `mention_only`

`group_trigger` is evaluated **before** mention extraction — it gates whether the message reaches the routing layer at all. The two settings compose cleanly:

```
Group message arrives
        ↓
ShouldRespondInGroup?   ← group_trigger / mention_only evaluated here
   No → drop
   Yes → continue
        ↓
ExtractAgentMention     ← trigger prefix parsed here
        ↓
RouteResolver           ← MentionedAgent used to select agent within binding
```

Existing `mention_only: true` behaviour (respond only when the bot account itself is @-mentioned by the platform) continues to work. The new mention routing operates at the content level after that gate.

---

## Semantic note on `mention_only`

Today `mention_only` means "the platform must signal a bot mention entity." With `agent_mentions` configured, operators will likely switch to `mention_only: false` and rely on the trigger prefix instead — the trigger prefix is the new mention mechanism. Both can coexist: `mention_only: true` still gates on platform-level bot mentions, and the trigger prefix further routes within that.

---

## Open Question — Shared History (Decision #7)

When Alice and Bob both participate in a group channel, they each hold independent session histories. Neither agent knows what the other said. Options:

- **Isolated (current default):** Each agent has its own history. Simple, no changes needed.
- **Shared read-only context:** Each response from any agent in the group is appended to a shared transcript that all agents in the binding receive as context prefix. Higher complexity; requires a new shared session store concept.
- **Agent delegation:** Alice can call Bob as a subagent (existing subagent mechanism) when she needs Bob's input. No shared history needed at the channel level.

Recommendation: start with isolated, document that agents can delegate via the existing subagent mechanism, revisit shared context as a follow-on.

---

## Out of Scope (v1)

- Agents addressing each other via trigger prefix in outbound messages
- Broadcast to all agents simultaneously
- Web UI @mention support (frontend concern)
- Mid-sentence mention routing (`"I think @alice should handle this"`)
