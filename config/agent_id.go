// ClawEh
// License: MIT

package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// defaultAgentID is the id an empty or unusable agent id normalizes to
// (routing.DefaultAgentID).
const defaultAgentID = "main"

// maxAgentIDLength is the longest agent id.
const maxAgentIDLength = 64

// asciiSpace is the whitespace trimmed before deciding an id is empty. It is
// ASCII only so the WebUI (agent-model.ts) trims exactly the same characters.
const asciiSpace = " \t\n\v\f\r"

var agentIDInvalidCharsRe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// The agent id rule. An agent id is 1 to 64 characters of ASCII letters
// (either case), 0-9, '-' and '_', and starts with a letter or digit.
// ValidAgentID checks it. Ids are case-insensitive: an agent's identity is
// its id in lower case, NormalizeAgentID, which is what the runtime uses for
// folders, sessions, jobs, devices and tokens, and what SameAgentID compares.
// The config keeps an id as the operator wrote it. The WebUI implements the
// identical rule (web/frontend/src/components/agents/agent-model.ts), and
// both are checked against testdata/agent_id_cases.json, and both quote an id
// in a refusal the same way (quoteAgentID, quoteAgentId).

// ValidAgentID reports whether id satisfies the agent id rule.
func ValidAgentID(id string) bool {
	if id == "" || len(id) > maxAgentIDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case (c == '-' || c == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

// NormalizeAgentID is an agent's identity: any input turned into a valid id
// in lower case. ASCII letters are lower-cased and the rest repaired as
// repairAgentID does; input that leaves nothing normalizes to "main". A valid
// id normalizes to its lower-case form. This is the rule the runtime routes
// by (routing.NormalizeAgentID delegates here).
func NormalizeAgentID(id string) string {
	if ValidAgentID(id) {
		return asciiLower(id)
	}
	if repaired := repairAgentID(id); repaired != "" {
		return asciiLower(repaired)
	}
	return defaultAgentID
}

// SameAgentID reports whether a and b name the same agent: neither is blank
// and their identities (NormalizeAgentID) are equal, so "Bob" and "bob" are
// the same agent. Every comparison of agent ids goes through it.
func SameAgentID(a, b string) bool {
	if strings.Trim(a, asciiSpace) == "" || strings.Trim(b, asciiSpace) == "" {
		return false
	}
	return NormalizeAgentID(a) == NormalizeAgentID(b)
}

// repairAgentID is the valid id suggested for id, keeping its case: every run
// of characters outside letters, digits, '-' and '_' becomes one '-', leading
// and trailing '-' and '_' are removed, and the result is cut to 64
// characters without a trailing '-' or '_'. It is "" when nothing is left.
func repairAgentID(id string) string {
	result := strings.Trim(agentIDInvalidCharsRe.ReplaceAllString(id, "-"), "-_")
	if len(result) > maxAgentIDLength {
		result = strings.TrimRight(result[:maxAgentIDLength], "-_")
	}
	return result
}

// asciiLower lower-cases A-Z only. Unicode case mapping differs between Go
// and JavaScript (U+0130 is one rune in Go, two in JavaScript), so the rule
// leaves every other character to become '-'.
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}

// Allows reports whether subagents.allow_agents lets its agent start or ask
// agentID: the list names the same agent (SameAgentID) or holds "*". A nil
// receiver or list allows nothing.
func (s *SubagentsConfig) Allows(agentID string) bool {
	if s == nil {
		return false
	}
	for _, allowed := range s.AllowAgents {
		if allowed == "*" || SameAgentID(allowed, agentID) {
			return true
		}
	}
	return false
}

// quoteAgentID quotes s for a refusal sentence. It is the WebUI's
// quoteAgentId, rune for rune, so the two give the same sentence: '"' and '\'
// are escaped with a backslash, tab, newline and carriage return are \t, \n
// and \r, and every other character that is a control or format character
// or a separator other than the ASCII space (Unicode Cc, Cf, Z) is \u
// followed by four lower-case hex digits, or \U and eight above U+FFFF.
// Everything else stands as itself. (strconv.Quote is not used: its rule
// follows Go's printable-character table, which the WebUI cannot reproduce.)
func quoteAgentID(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r != ' ' && unicode.In(r, unicode.Cc, unicode.Cf, unicode.Z):
			if r > 0xFFFF {
				fmt.Fprintf(&b, `\U%08x`, r)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// agentIDError says why id, an agent id or a reference to one, does not
// satisfy the agent id rule (ValidAgentID), or returns nil when it does. where names the
// place a reference was found (" in bindings"); it is empty for
// agents.list[].id. ClawEh never rewrites an id: the operator fixes it.
func agentIDError(id, where string) error {
	// The messages are sentences an operator reads (WebUI, startup), built by
	// concatenation like the other config refusals (mount_names.go).
	if ValidAgentID(id) {
		return nil
	}
	trimmed := strings.Trim(id, asciiSpace)
	if trimmed == "" {
		if where == "" {
			return errors.New("An agent has no id; give it one, such as " + quoteAgentID("alice") + ".")
		}
		return errors.New("An agent id" + where + " is empty; name the agent, such as " + quoteAgentID("alice") + ".")
	}
	suggestion := repairAgentID(id)
	var problem string
	switch {
	case len(trimmed) > maxAgentIDLength && !agentIDInvalidCharsRe.MatchString(trimmed):
		problem = "is longer than " + strconv.Itoa(maxAgentIDLength) + " characters"
	case (trimmed[0] == '-' || trimmed[0] == '_') && !agentIDInvalidCharsRe.MatchString(trimmed):
		problem = "must start with a letter or digit"
	default:
		problem = "may use only letters, digits, - and _"
	}
	msg := "Agent id " + quoteAgentID(id) + where + " " + problem
	if suggestion == "" {
		// Nothing of the id survives; any suggestion would be a guess.
		return errors.New(msg + ".")
	}
	return errors.New(msg + "; use " + quoteAgentID(suggestion) + ".")
}

// duplicateAgentIDError is the refusal of agents.list[].ids that name one
// agent: spellings holds each different way they were written, in order.
func duplicateAgentIDError(spellings []string) error {
	if len(spellings) == 1 {
		return errors.New("Agent id " + quoteAgentID(spellings[0]) + " is used twice; give each agent its own id.")
	}
	quoted := make([]string, len(spellings))
	for i, s := range spellings {
		quoted[i] = quoteAgentID(s)
	}
	last := len(quoted) - 1
	return errors.New("Agent ids " + strings.Join(quoted[:last], ", ") + " and " + quoted[last] +
		" name the same agent; give each agent its own id.")
}

// AgentIDErrors returns one error for each agent id, or reference to one, that
// does not satisfy the agent id rule (ValidAgentID): agents.list[].id,
// bindings[].agent_id and agent_mentions, and subagents.allow_agents ("*"
// allowed), and one for each agent that more than one agents.list[].id names
// (ids are compared ignoring case, SameAgentID).
// A binding with no agent_id routes to the default agent and is allowed.
// LoadConfig and Store.Update refuse a config with any.
func (c *Config) AgentIDErrors() []error {
	var errs []error
	add := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	// Ids are compared by identity, so "Bob" and "bob" are one agent.
	type use struct {
		count     int
		spellings []string
	}
	uses := make(map[string]*use, len(c.Agents.List))
	var order []string
	for i := range c.Agents.List {
		a := &c.Agents.List[i]
		add(agentIDError(a.ID, ""))
		if ValidAgentID(a.ID) {
			key := NormalizeAgentID(a.ID)
			u := uses[key]
			if u == nil {
				u = &use{}
				uses[key] = u
				order = append(order, key)
			}
			u.count++
			if !slices.Contains(u.spellings, a.ID) {
				u.spellings = append(u.spellings, a.ID)
			}
		}
		if a.Subagents == nil {
			continue
		}
		for _, ref := range a.Subagents.AllowAgents {
			if ref != "*" {
				add(agentIDError(ref, " in "+a.DisplayName()+"'s subagents.allow_agents"))
			}
		}
	}
	for i := range c.Bindings {
		b := &c.Bindings[i]
		if b.AgentID != "" {
			add(agentIDError(b.AgentID, " in bindings"))
		}
		for _, ref := range b.AgentMentions {
			if ref != "*" {
				add(agentIDError(ref, " in bindings agent_mentions"))
			}
		}
	}
	for _, key := range order {
		if u := uses[key]; u.count > 1 {
			add(duplicateAgentIDError(u.spellings))
		}
	}
	return errs
}
