// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package ccv3

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	ccv3pkg "github.com/PivotLLM/ClawEh/ccv3"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/tools"
)

// The actions, in the order the description and refusals name them.
var actions = []string{"list", "get", "create", "update", "rename", "copy", "delete"}

// actionArgs are the arguments each action takes besides action.
var actionArgs = map[string][]string{
	"list":   nil,
	"get":    {"id"},
	"create": {"id", "card"},
	"update": {"id", "changes"},
	"rename": {"id", "new_id"},
	"copy":   {"id", "new_id"},
	"delete": {"id"},
}

// description is the tool's guide (there is no separate readme).
const description = `Manage the character-card library (Character Card V3). A card describes a character an agent can play: who it is, how it speaks, its world. ` +
	`Actions: list; get {id}; create {id, card}; update {id, changes}; rename {id, new_id}; copy {id, new_id}; delete {id}. ` +
	`Ids are 1 to 64 letters, digits, - and _, starting with a letter or digit; case does not matter. ` +
	`Fields of card: name (required); nickname; description; personality; scenario; first_mes, alternate_greetings, group_only_greetings (greetings); ` +
	`mes_example (example dialogue, <START> between examples); system_prompt, post_history_instructions (instructions for the model); ` +
	`tags, creator, character_version, source, creator_notes, creator_notes_multilingual (about the card); ` +
	`character_book (lorebook entries: keys, content, enabled, insertion_order); assets, extensions (kept, not used). creation_date and modification_date are set automatically. ` +
	`In card text {{char}} becomes the character's name, {{user}} becomes "the user" and {{// note}} is removed. ` +
	`The schema is strict: unknown fields are refused. changes is an RFC 7386 merge patch: null removes a field, objects merge, arrays are replaced whole. ` +
	`Cards are attached to agents by the operator in the WebUI, by agent_spawn's character_card, or by a forum participant's character_card; an attached card cannot be renamed or deleted.`

var parameters = []global.Parameter{
	{
		Name: "action", Type: "string", Required: true, Description: "list, get, create, update, rename, copy or delete",
		Enum: actionEnum(),
	},
	{Name: "id", Type: "string", Description: "The card id; every action but list"},
	{Name: "new_id", Type: "string", Description: "rename and copy: the new card id"},
	{Name: "card", Type: "object", Description: "create: the card's data object"},
	{Name: "changes", Type: "object", Description: "update: an RFC 7386 merge patch over the card's data object; null removes a field, arrays are replaced whole"},
}

// definition is the ccv3 tool of t.
func definition(t *tool) global.ToolDefinition {
	schema := global.ParametersToSchema(parameters)
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		panic("ccv3: tool schema without properties")
	}
	for name, arg := range map[string]map[string]any{"card": ccv3pkg.CardSchema(), "changes": ccv3pkg.PatchSchema()} {
		arg = maps.Clone(arg)
		if p, ok := props[name].(map[string]any); ok {
			arg["description"] = p["description"]
		}
		props[name] = arg
	}
	return global.ToolDefinition{
		Name:        Suite,
		Description: description,
		Parameters:  parameters,
		RawSchema:   schema,
		Handler:     t.handle,
	}
}

// tool runs the ccv3 actions for one agent. cfg is the configuration the
// tool was built with; agentID is the identity the tool acts as.
type tool struct {
	cfg     *config.Config
	agentID string
}

// refused is a refusal result: one sentence for the model, logged by the
// host as an expected refusal.
func refused(err error) *global.Result {
	if !tools.IsExpectedRefusal(err) {
		err = tools.Refusal(err)
	}
	return &global.Result{ForLLM: err.Error(), IsError: true, Err: err}
}

func refusedf(format string, args ...any) *global.Result {
	return refused(fmt.Errorf(format, args...))
}

// failed reports an error from the store: a refusal as its own sentence,
// anything else (an I/O failure) in a fixed sentence with the detail kept
// for the log.
func failed(err error, action, id string) *global.Result {
	if tools.IsExpectedRefusal(err) {
		return refused(err)
	}
	return &global.Result{ForLLM: fmt.Sprintf("Card %q could not be %s; try again later.", id, pastTense[action]), IsError: true, Err: err}
}

var pastTense = map[string]string{
	"list": "listed", "get": "read", "create": "created", "update": "updated",
	"rename": "renamed", "copy": "copied", "delete": "deleted",
}

func (t *tool) handle(call *global.ToolCall) (*global.Result, error) {
	return t.run(call), nil
}

// run performs one call; every outcome, refusals and failures included, is
// a result.
func (t *tool) run(call *global.ToolCall) *global.Result {
	if res, ok := unknownArgs(call.Args); ok {
		return res
	}
	action, ok := call.Args["action"].(string)
	if !ok || action == "" {
		return refusedf("The action argument is required: %s.", actionList())
	}
	takes, ok := actionArgs[action]
	if !ok {
		return refusedf("Unknown action %q; use %s.", action, actionList())
	}
	for name := range call.Args {
		if name != "action" && !slices.Contains(takes, name) {
			return refusedf("The %s action does not take %s.", action, name)
		}
	}
	args := map[string]string{}
	for _, name := range []string{"id", "new_id"} {
		if !slices.Contains(takes, name) {
			continue
		}
		v, ok := call.Args[name].(string)
		if _, present := call.Args[name]; present && !ok {
			return refusedf("The %s argument must be text.", name)
		}
		if strings.TrimSpace(v) == "" {
			return refusedf("The %s argument is required for %s.", name, action)
		}
		args[name] = v
	}
	for _, name := range []string{"card", "changes"} {
		if slices.Contains(takes, name) && call.Args[name] == nil {
			return refusedf("The %s argument is required for %s: a JSON object.", name, action)
		}
	}

	store, err := ccv3pkg.Open(t.cfg.CardsPath())
	if err != nil {
		return &global.Result{ForLLM: "The card library could not be opened; try again later.", IsError: true, Err: err}
	}
	id, newID := args["id"], args["new_id"]
	switch action {
	case "list":
		return t.list(store)
	case "get":
		card, err := store.Get(id)
		if err != nil {
			return failed(err, action, id)
		}
		return jsonResult(card, action, id)
	case "create":
		data, err := marshal(call.Args["card"])
		if err != nil {
			return refusedf("The card argument cannot be encoded as JSON.")
		}
		card, err := store.Create(id, data)
		if err != nil {
			return failed(err, action, id)
		}
		t.logWrite(action, id, "", len(card.Data))
		return jsonResult(card, action, id)
	case "update":
		patch, err := marshal(call.Args["changes"])
		if err != nil {
			return refusedf("The changes argument cannot be encoded as JSON.")
		}
		card, err := store.Update(id, patch)
		if err != nil {
			return failed(err, action, id)
		}
		t.logWrite(action, id, "", len(card.Data))
		return jsonResult(card, action, id)
	case "rename":
		if res, ok := t.refuseAttached(id); ok {
			return res
		}
		if err := store.Rename(id, newID); err != nil {
			return failed(err, action, id)
		}
		t.logWrite(action, id, newID, 0)
		return jsonResult(map[string]string{"id": newID, "previous_id": id}, action, id)
	case "copy":
		if err := store.Copy(id, newID); err != nil {
			return failed(err, action, id)
		}
		t.logWrite(action, id, newID, 0)
		return jsonResult(map[string]string{"id": newID, "copied_from": id}, action, id)
	default: // delete
		if res, ok := t.refuseAttached(id); ok {
			return res
		}
		if err := store.Delete(id); err != nil {
			return failed(err, action, id)
		}
		t.logWrite(action, id, "", 0)
		return jsonResult(map[string]string{"deleted": id}, action, id)
	}
}

// listEntry is one card of the list action.
type listEntry struct {
	ccv3pkg.Summary
	AttachedTo []string `json:"attached_to"`
}

func (t *tool) list(store *ccv3pkg.Store) *global.Result {
	summaries, invalid, err := store.List()
	if err != nil {
		return &global.Result{ForLLM: "The card library could not be listed; try again later.", IsError: true, Err: err}
	}
	cards := make([]listEntry, len(summaries))
	for i, s := range summaries {
		cards[i] = listEntry{Summary: s, AttachedTo: []string{}}
		for _, a := range t.attached(s.ID) {
			cards[i].AttachedTo = append(cards[i].AttachedTo, a.ID)
		}
	}
	if invalid == nil {
		invalid = []ccv3pkg.Invalid{}
	}
	return jsonResult(map[string]any{"cards": cards, "invalid": invalid}, "list", "")
}

// attached is every configured agent whose character_card names card id.
func (t *tool) attached(id string) []*config.AgentConfig {
	var out []*config.AgentConfig
	for i := range t.cfg.Agents.List {
		a := &t.cfg.Agents.List[i]
		if a.CharacterCard != "" && ccv3pkg.ValidID(a.CharacterCard) && ccv3pkg.NormalizeID(a.CharacterCard) == ccv3pkg.NormalizeID(id) {
			out = append(out, a)
		}
	}
	return out
}

// refuseAttached refuses renaming or deleting a card a configured agent
// names.
func (t *tool) refuseAttached(id string) (*global.Result, bool) {
	agents := t.attached(id)
	if len(agents) == 0 {
		return nil, false
	}
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = a.DisplayName()
	}
	noun := "agent"
	if len(names) > 1 {
		noun = "agents"
	}
	res := refusedf("Card %q is attached to %s %s; detach it first.", id, noun, strings.Join(names, ", "))
	return res, true
}

func (t *tool) logWrite(action, id, newID string, size int) {
	fields := map[string]any{"agent": t.agentID, "action": action, "card": id}
	if newID != "" {
		fields["new_id"] = newID
	}
	if size > 0 {
		fields["bytes"] = size
	}
	logger.InfoCF("ccv3", "Card "+pastTense[action], fields)
}

// unknownArgs refuses an argument the tool does not declare.
func unknownArgs(args map[string]any) (*global.Result, bool) {
	var unknown []string
	for name := range args {
		if !slices.ContainsFunc(parameters, func(p global.Parameter) bool { return p.Name == name }) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return nil, false
	}
	slices.Sort(unknown)
	msg := "Unknown argument " + unknown[0]
	if len(unknown) > 1 {
		msg = "Unknown arguments " + strings.Join(unknown, ", ")
	}
	res := refused(errors.New(msg + "; use action, id, new_id, card or changes."))
	return res, true
}

func actionEnum() []any {
	out := make([]any, len(actions))
	for i, a := range actions {
		out[i] = a
	}
	return out
}

func actionList() string {
	return strings.Join(actions[:len(actions)-1], ", ") + " or " + actions[len(actions)-1]
}

// marshal encodes v as compact JSON without HTML escaping (card text such
// as <START> stays readable in results).
func marshal(v any) (json.RawMessage, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

func jsonResult(v any, action, id string) *global.Result {
	data, err := marshal(v)
	if err != nil {
		return &global.Result{ForLLM: fmt.Sprintf("Card %q could not be %s; try again later.", id, pastTense[action]), IsError: true, Err: err}
	}
	return &global.Result{ForLLM: string(data)}
}
