// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"errors"
	"strings"
	"testing"
)

// The example configuration's layers: [0] review (alice, bob; json "findings"),
// [1] debate (alice, bob; text; moderator chair), [2] report (editor;
// markdown).

func TestValidateStaticExample(t *testing.T) {
	if err := validateStatic(cfgtExample(t)); err != nil {
		t.Fatalf("the example configuration must validate: %v", err)
	}
}

func cfgtDisable(c *Config, i int) {
	off := false
	c.Layers[i].Enabled = &off
}

func TestValidateStaticRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(c *Config)
		path   string
		want   []string
	}{
		// brief
		{"purpose missing", func(c *Config) { c.Brief.Purpose = "" }, "brief.purpose", []string{"required"}},
		{"task blank", func(c *Config) { c.Brief.Task = "  \n" }, "brief.task", []string{"required"}},

		// sources
		{"source ID syntax", func(c *Config) { c.Sources["1st"] = Source{Decode: FormatText, Inline: cfgtRaw(`"x"`)} }, "sources.1st", []string{`"1st"`, "not starting with a digit"}},
		{"source decode unknown", func(c *Config) { c.Sources["report"] = Source{Decode: "yaml", Inline: cfgtRaw(`"x"`)} }, "sources.report.decode", []string{`"yaml"`}},
		{"source decode missing", func(c *Config) { c.Sources["report"] = Source{Inline: cfgtRaw(`"x"`)} }, "sources.report.decode", []string{"not one of"}},
		{"source inline and file", func(c *Config) {
			c.Sources["report"] = Source{Decode: FormatText, Inline: cfgtRaw(`"x"`), File: "r.txt"}
		}, "sources.report", []string{"both inline and file"}},
		{"source neither", func(c *Config) { c.Sources["report"] = Source{Decode: FormatText} }, "sources.report", []string{"exactly one of inline or file"}},
		{"text inline not a string", func(c *Config) { c.Sources["report"] = Source{Decode: FormatText, Inline: cfgtRaw(`{"a":1}`)} }, "sources.report.inline", []string{"JSON string", `"text"`}},
		{"markdown inline not a string", func(c *Config) { c.Sources["report"] = Source{Decode: FormatMarkdown, Inline: cfgtRaw(`3`)} }, "sources.report.inline", []string{"JSON string"}},
		{"file absolute", func(c *Config) { c.Sources["report"] = Source{Decode: FormatText, File: "/etc/passwd"} }, "sources.report.file", []string{"relative"}},
		{"file escapes", func(c *Config) { c.Sources["report"] = Source{Decode: FormatText, File: "docs/../../secret.txt"} }, "sources.report.file", []string{`".."`}},
		{"file is dot-dot", func(c *Config) { c.Sources["report"] = Source{Decode: FormatText, File: ".."} }, "sources.report.file", []string{`".."`}},
		{"source IDs differing in case", func(c *Config) { c.Sources["Report"] = Source{Decode: FormatText, Inline: cfgtRaw(`"x"`)} }, "sources.report", []string{`"Report"`, `"report"`, "letter case"}},

		// participants
		{"no participants", func(c *Config) { c.Participants = nil }, "participants", []string{"at least one"}},
		{"participant ID syntax", func(c *Config) { c.Participants["a.b"] = Participant{Model: "default"} }, "participants.a.b", []string{`"a.b"`}},
		{"agent and clone", func(c *Config) { c.Participants["alice"] = Participant{Agent: "alice", Clone: "alice"} }, "participants.alice", []string{"both agent and clone"}},
		{"model on an existing agent", func(c *Config) { c.Participants["alice"] = Participant{Agent: "alice", Model: "large"} }, "participants.alice.model", []string{`"alice"`, "own model"}},
		{"no form", func(c *Config) { c.Participants["alice"] = Participant{Instructions: "x"} }, "participants.alice", []string{"exactly one of agent, clone or model"}},
		{"system_prompt on agent", func(c *Config) { c.Participants["alice"] = Participant{Agent: "alice", SystemPrompt: "x"} }, "participants.alice.system_prompt", []string{"fresh"}},
		{"system_prompt on clone", func(c *Config) { c.Participants["bob"] = Participant{Clone: "bob", SystemPrompt: "x"} }, "participants.bob.system_prompt", []string{"fresh"}},
		{"mode on clone", func(c *Config) { c.Participants["bob"] = Participant{Clone: "bob", Mode: FreshModeContext} }, "participants.bob.mode", []string{"fresh"}},
		{"unknown mode", func(c *Config) { c.Participants["chair"] = Participant{Model: "default", Mode: "forever"} }, "participants.chair.mode", []string{`"forever"`, "single_shot"}},
		{"participant IDs differing in case", func(c *Config) { c.Participants["Alice"] = Participant{Model: "default"} }, "participants.alice", []string{`"Alice"`, `"alice"`, "letter case"}},
		{"one real agent twice", func(c *Config) { c.Participants["editor"] = Participant{Agent: "alice"} }, "participants.editor.agent", []string{`"alice"`, `"editor"`, "one seat"}},
		{"moderator is a participant's real agent", func(c *Config) { c.Participants["chair"] = Participant{Agent: "alice"} }, "participants.chair.agent", []string{`"alice"`, `"chair"`, "one seat"}},
		{"name equals another's ID", func(c *Config) { c.Participants["editor"] = Participant{Model: "default", Name: "ALICE"} }, "participants.editor.name", []string{`"alice"`, `"editor"`, `"ALICE"`}},
		{"names equal ignoring case", func(c *Config) {
			c.Participants["chair"] = Participant{Model: "default", Name: "Reviewer"}
			c.Participants["editor"] = Participant{Model: "default", Name: "reviewer"}
		}, "participants.editor.name", []string{`"chair"`, `"editor"`, `"reviewer"`}},

		// schemas
		{"schema ID syntax", func(c *Config) { c.Schemas["2x"] = cfgtRaw(`{}`) }, "schemas.2x", []string{`"2x"`}},
		{"schema not an object", func(c *Config) { c.Schemas["findings"] = cfgtRaw(`[]`) }, "schemas.findings", []string{"JSON object"}},
		{"boolean schema", func(c *Config) { c.Schemas["findings"] = cfgtRaw(`true`) }, "schemas.findings", []string{"JSON object"}},

		// limits
		{"max_calls zero", func(c *Config) { c.Limits.MaxCalls = 0 }, "limits.max_calls", []string{"positive", "got 0"}},
		{"max_duration negative", func(c *Config) { c.Limits.MaxDurationSeconds = -1 }, "limits.max_duration_seconds", []string{"positive", "got -1"}},
		{"call_timeout zero", func(c *Config) { c.Limits.CallTimeoutSeconds = 0 }, "limits.call_timeout_seconds", []string{"positive"}},
		{"attempts zero", func(c *Config) { c.Limits.MaxAttemptsPerTurn = 0 }, "limits.max_attempts_per_turn", []string{"positive"}},
		{"parallel zero", func(c *Config) { c.Limits.MaxParallelCalls = 0 }, "limits.max_parallel_calls", []string{"positive"}},

		// layers
		{"no layers", func(c *Config) { c.Layers = nil; c.ResultLayers = nil }, "layers", []string{"at least one layer"}},
		{"all disabled", func(c *Config) {
			for i := range c.Layers {
				cfgtDisable(c, i)
			}
			c.ResultLayers = nil
		}, "layers", []string{"every layer is disabled"}},
		{"layer ID syntax", func(c *Config) { c.Layers[0].ID = "re view"; c.Layers[1].Inputs = c.Layers[1].Inputs[:1] }, "layers[0].id", []string{`"re view"`}},
		{"duplicate layer ID", func(c *Config) { c.Layers[2].ID = "review"; c.ResultLayers = nil }, "layers[2].id", []string{`"review"`, "layers[0]"}},
		{"layer IDs differing in case", func(c *Config) { c.Layers[2].ID = "Review"; c.ResultLayers = nil }, "layers[2].id", []string{`"review"`, `"Review"`, "layers[0]", "letter case"}},
		{"layer without participants", func(c *Config) { c.Layers[2].Participants = nil }, "layers[2].participants", []string{`"report"`, "at least one"}},
		{"layer participant unknown", func(c *Config) { c.Layers[0].Participants = []string{"alice", "carol"} }, "layers[0].participants[1]", []string{`"carol"`, "not configured"}},
		{"layer participant twice", func(c *Config) { c.Layers[0].Participants = []string{"alice", "alice"} }, "layers[0].participants[1]", []string{`"alice"`, "twice"}},
		{"layer instructions missing", func(c *Config) { c.Layers[1].Instructions = "" }, "layers[1].instructions", []string{`"debate"`, "required"}},
		{"delivery missing", func(c *Config) { c.Layers[0].Delivery = "" }, "layers[0].delivery", []string{"after_round, per_turn"}},
		{"delivery unknown", func(c *Config) { c.Layers[0].Delivery = "eventually" }, "layers[0].delivery", []string{`"eventually"`}},
		{"max_rounds zero", func(c *Config) { c.Layers[0].MaxRounds = 0 }, "layers[0].max_rounds", []string{`"review"`, "positive"}},
		{"layer max_calls negative", func(c *Config) { c.Layers[1].MaxCalls = -2 }, "layers[1].max_calls", []string{"positive", "-2"}},
		{"layer max_calls above forum", func(c *Config) { c.Layers[1].MaxCalls = 31 }, "layers[1].max_calls", []string{"31", "limits.max_calls (30)"}},

		// output
		{"output format unknown", func(c *Config) { c.Layers[2].Output.Format = "html" }, "layers[2].output.format", []string{`"html"`}},
		{"output format missing", func(c *Config) { c.Layers[2].Output.Format = "" }, "layers[2].output.format", []string{"not one of"}},
		{"schema on text output", func(c *Config) { c.Layers[1].Output.Schema = "findings" }, "layers[1].output.schema", []string{"only to format json"}},
		{"share on markdown output", func(c *Config) { c.Layers[2].Output.Share = cfgtStrs() }, "layers[2].output.share", []string{"only to format json"}},
		{"output schema unknown", func(c *Config) { c.Layers[0].Output.Schema = "verdict" }, "layers[0].output.schema", []string{`"verdict"`, "not configured"}},

		// routes: producer
		{"from malformed", func(c *Config) { c.Layers[0].Inputs[0].From = "report" }, "layers[0].inputs[0].from", []string{`"report"`}},
		{"from unknown kind", func(c *Config) { c.Layers[0].Inputs[0].From = "file:report" }, "layers[0].inputs[0].from", []string{`"file"`}},
		{"unknown source", func(c *Config) { c.Layers[0].Inputs[0].From = "source:memo" }, "layers[0].inputs[0].from", []string{`source "memo"`}},
		{"unknown layer", func(c *Config) { c.Layers[1].Inputs[1].From = "layer:draft" }, "layers[1].inputs[1].from", []string{`layer "draft"`, "not configured"}},
		{"forward reference", func(c *Config) { c.Layers[0].Inputs = append(c.Layers[0].Inputs, Route{From: "layer:report"}) }, "layers[0].inputs[1].from", []string{`"report"`, `before layer "review"`}},
		{"self reference", func(c *Config) { c.Layers[1].Inputs[1].From = "layer:debate" }, "layers[1].inputs[1].from", []string{`"debate"`, "backward"}},
		{"required route from disabled layer", func(c *Config) { cfgtDisable(c, 0) }, "layers[1].inputs[1].from", []string{`"review"`, "disabled", "optional"}},

		// routes: enumerations
		{"select unknown", func(c *Config) { c.Layers[1].Inputs[1].Select = "first" }, "layers[1].inputs[1].select", []string{`"first"`}},
		{"view unknown", func(c *Config) { c.Layers[1].Inputs[1].View = "raw" }, "layers[1].inputs[1].view", []string{`"raw"`}},
		{"distribute unknown", func(c *Config) { c.Layers[1].Inputs[1].Distribute = "each" }, "layers[1].inputs[1].distribute", []string{`"each"`}},

		// routes: source restrictions
		{"select on source", func(c *Config) { c.Layers[0].Inputs[0].Select = SelectAll }, "layers[0].inputs[0].select", []string{"layer inputs"}},
		{"authors on source", func(c *Config) { c.Layers[0].Inputs[0].Authors = []string{"alice"} }, "layers[0].inputs[0].authors", []string{"no author"}},
		{"view on source", func(c *Config) { c.Layers[0].Inputs[0].View = ViewFull; c.Layers[0].Inputs[0].To = []string{"alice"} }, "layers[0].inputs[0].view", []string{"layer inputs"}},
		{"same_participant on source", func(c *Config) { c.Layers[0].Inputs[0].Distribute = DistributeSameParticipant }, "layers[0].inputs[0].distribute", []string{"same_participant", "no author"}},
		{"anonymous on source", func(c *Config) { c.Layers[0].Inputs[0].Anonymous = true }, "layers[0].inputs[0].anonymous", []string{"layer inputs", "no author"}},
		{"anonymous with same_participant", func(c *Config) {
			c.Layers[1].Inputs[1].Anonymous = true
			c.Layers[1].Inputs[1].Distribute = DistributeSameParticipant
		}, "layers[1].inputs[1].anonymous", []string{"own outputs", "same_participant"}},
		// routes: anonymous reads (layers[2] is report, reading review in
		// inputs[1] and debate in inputs[2])
		{"anonymous in a per_turn layer", func(c *Config) {
			c.Layers[2].Inputs[1].Anonymous = true
			c.Layers[2].Delivery = DeliveryPerTurn
		}, "layers[2].inputs[1].anonymous", []string{"Layer report reads review anonymously", "after_round with one round and no moderator"}},
		{"anonymous in a multi-round layer", func(c *Config) {
			c.Layers[2].Inputs[1].Anonymous = true
			c.Layers[2].MaxRounds = 2
		}, "layers[2].inputs[1].anonymous", []string{"Layer report reads review anonymously"}},
		{"anonymous in a moderated layer", func(c *Config) {
			c.Layers[2].Inputs[1].Anonymous = true
			c.Layers[2].Moderator = &Moderator{Participant: "chair", AfterRound: 1, EveryRounds: 1}
		}, "layers[2].inputs[1].anonymous", []string{"Layer report reads review anonymously"}},
		{"anonymous and by name", func(c *Config) {
			c.Layers[2].Inputs[1].Anonymous = true
			c.Layers[2].Inputs = append(c.Layers[2].Inputs, Route{From: "layer:review", Optional: true})
		}, "layers[2].inputs[1].anonymous", []string{"editor reads layer review anonymously in layer report and by name in layer report"}},
		{"anonymous read of a per_turn layer one takes part in", func(c *Config) {
			c.Layers[2].Participants = []string{"alice"}
			c.Layers[2].Inputs[2].Anonymous = true
		}, "layers[2].inputs[2].anonymous", []string{"alice takes part in layer debate", "per_turn or has more than one round"}},
		{"anonymous read of a multi-round layer one takes part in", func(c *Config) {
			c.Layers[1].Delivery = DeliveryAfterRound
			c.Layers[2].Participants = []string{"alice"}
			c.Layers[2].Inputs[2].Anonymous = true
		}, "layers[2].inputs[2].anonymous", []string{"alice takes part in layer debate"}},
		{"anonymous read of a layer one moderates", func(c *Config) {
			c.Layers[2].Participants = []string{"chair"}
			c.Layers[2].Inputs[2].Anonymous = true
		}, "layers[2].inputs[2].anonymous", []string{"chair moderates layer debate"}},
		{"anonymous read of only one's own outputs", func(c *Config) {
			c.Layers[2].Participants = []string{"alice"}
			c.Layers[2].Inputs[1].Anonymous = true
			c.Layers[2].Inputs[1].Authors = []string{"alice"}
		}, "layers[2].inputs[1].anonymous", []string{"alice reads layer review anonymously in layer report but would only see its own responses"}},
		{"anonymous random with only one's own outputs", func(c *Config) {
			c.Layers[2].Participants = []string{"alice", "editor"}
			c.Layers[2].Inputs[1].Anonymous = true
			c.Layers[2].Inputs[1].Authors = []string{"alice"}
			c.Layers[2].Inputs[1].Distribute = DistributeRandom
			c.Layers[2].Inputs[1].Optional = true
		}, "layers[2].inputs[1].anonymous", []string{"alice", "only see its own responses"}},
		{"random source to two recipients", func(c *Config) { c.Layers[0].Inputs[0].Distribute = DistributeRandom }, "layers[0].inputs[0].distribute", []string{`"report"`, "2 recipients", "optional"}},

		// routes: recipients and authors
		{"view full without to", func(c *Config) { c.Layers[1].Inputs[1].View = ViewFull }, "layers[1].inputs[1].view", []string{"to"}},
		{"to outsider", func(c *Config) { c.Layers[1].Inputs[1].To = []string{"editor"} }, "layers[1].inputs[1].to[0]", []string{`"editor"`, `layer "debate"`}},
		{"to twice", func(c *Config) { c.Layers[1].Inputs[1].To = []string{"bob", "bob"} }, "layers[1].inputs[1].to[1]", []string{`"bob"`, "twice"}},
		{"author outsider", func(c *Config) { c.Layers[2].Inputs[1].Authors = []string{"editor"} }, "layers[2].inputs[1].authors[0]", []string{`"editor"`, `layer "review"`}},
		{"author twice", func(c *Config) { c.Layers[2].Inputs[1].Authors = []string{"bob", "bob"} }, "layers[2].inputs[1].authors[1]", []string{"twice"}},
		{"same_participant recipient not a producer", func(c *Config) { c.Layers[2].Inputs[1].Distribute = DistributeSameParticipant }, "layers[2].inputs[1].distribute", []string{`"editor"`, `layer "review"`}},

		// routes: paths
		{"paths on text source", func(c *Config) { c.Layers[0].Inputs[0].Paths = []string{"/a"} }, "layers[0].inputs[0].paths", []string{`"source:report"`, "not JSON"}},
		{"paths on text layer", func(c *Config) { c.Layers[2].Inputs[2].Paths = []string{"/a"} }, "layers[2].inputs[2].paths", []string{`"layer:debate"`, "not JSON"}},
		{"paths outside share", func(c *Config) {
			c.Layers[0].Output.Share = cfgtStrs("/agreements")
			c.Layers[2].Inputs[1].Paths = []string{"/disagreements"}
		}, "layers[2].inputs[1].paths", []string{`"/disagreements"`, `layer "review"`, "share"}},
		{"paths with empty share", func(c *Config) {
			c.Layers[0].Output.Share = cfgtStrs()
			c.Layers[2].Inputs[1].Paths = []string{"/agreements"}
		}, "layers[2].inputs[1].paths", []string{`"/agreements"`}},
		{"paths sibling prefix is not within share", func(c *Config) {
			c.Layers[0].Output.Share = cfgtStrs("/agree")
			c.Layers[2].Inputs[1].Paths = []string{"/agreements"}
		}, "layers[2].inputs[1].paths", []string{`"/agreements"`}},

		// moderator
		{"moderator participant missing", func(c *Config) { c.Layers[1].Moderator.Participant = "" }, "layers[1].moderator.participant", []string{"required"}},
		{"moderator unknown", func(c *Config) { c.Layers[1].Moderator.Participant = "carol" }, "layers[1].moderator.participant", []string{`"carol"`, "not configured"}},
		{"moderator moderates itself", func(c *Config) { c.Layers[1].Moderator.Participant = "alice" }, "layers[1].moderator.participant", []string{`"alice"`, "cannot also moderate"}},
		{"after_round zero", func(c *Config) { c.Layers[1].Moderator.AfterRound = 0 }, "layers[1].moderator.after_round", []string{"positive"}},
		{"after_round at max_rounds", func(c *Config) { c.Layers[1].Moderator.AfterRound = 3 }, "layers[1].moderator.after_round", []string{"below max_rounds (3)"}},
		{"every_rounds zero", func(c *Config) { c.Layers[1].Moderator.EveryRounds = 0 }, "layers[1].moderator.every_rounds", []string{"positive"}},
		{"conversation_view unknown", func(c *Config) { c.Layers[1].Moderator.ConversationView = "summary" }, "layers[1].moderator.conversation_view", []string{`"summary"`}},
		{"moderator schema unknown", func(c *Config) { c.Layers[1].Moderator.Schema = "notes" }, "layers[1].moderator.schema", []string{`"notes"`, "not configured"}},
		{"moderator input with to", func(c *Config) { c.Layers[1].Moderator.Inputs[0].To = []string{"alice"} }, "layers[1].moderator.inputs[0].to", []string{"moderator only"}},
		{"moderator input random", func(c *Config) {
			c.Layers[1].Moderator.Inputs = []Route{{From: "layer:review", Distribute: DistributeRandom}}
		}, "layers[1].moderator.inputs[0].distribute", []string{"must be all"}},
		{"moderator input same_participant", func(c *Config) {
			c.Layers[1].Moderator.Inputs = []Route{{From: "layer:review", Distribute: DistributeSameParticipant}}
		}, "layers[1].moderator.inputs[0].distribute", []string{"must be all"}},
		{"moderator input forward", func(c *Config) { c.Layers[1].Moderator.Inputs[0].From = "layer:report" }, "layers[1].moderator.inputs[0].from", []string{`"report"`, "before"}},
		{"moderator input unknown source", func(c *Config) { c.Layers[1].Moderator.Inputs[0].From = "source:rubric" }, "layers[1].moderator.inputs[0].from", []string{`"rubric"`}},

		// result_layers
		{"result layer unknown", func(c *Config) { c.ResultLayers = []string{"summary"} }, "result_layers[0]", []string{`"summary"`, "not configured"}},
		{"result layer disabled", func(c *Config) {
			cfgtDisable(c, 1)
			c.Layers[2].Inputs = c.Layers[2].Inputs[:2]
			c.ResultLayers = []string{"report", "debate"}
		}, "result_layers[1]", []string{`"debate"`, "disabled"}},
		{"result layer twice", func(c *Config) { c.ResultLayers = []string{"report", "report"} }, "result_layers[1]", []string{"twice"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := cfgtExample(t)
			tt.mutate(cfg)
			cfgtWantIssue(t, validateStatic(cfg), tt.path, tt.want...)
		})
	}
}

func TestValidateStaticAccepts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(c *Config)
	}{
		{"fresh modes", func(c *Config) {
			c.Participants["chair"] = Participant{Model: "default", Mode: FreshModeSingleShot, SystemPrompt: "You chair."}
			c.Participants["editor"] = Participant{Model: "default", Mode: FreshModeContext}
		}},
		{"clone with a model and a name", func(c *Config) {
			c.Participants["bob"] = Participant{Clone: "bob", Model: "large", Name: "Bob (clone)"}
		}},
		{"no instructions", func(c *Config) { c.Participants["alice"] = Participant{Agent: "alice"} }},
		{"anonymous layer input", func(c *Config) { c.Layers[2].Inputs[1].Anonymous = true }},
		{"anonymous random", func(c *Config) {
			c.Layers[2].Participants = []string{"editor", "chair"}
			c.Layers[2].Inputs[1].Anonymous = true
			c.Layers[2].Inputs[1].Distribute = DistributeRandom
		}},
		{"two clones of one agent", func(c *Config) {
			c.Participants["chair"] = Participant{Clone: "alice"}
			c.Participants["editor"] = Participant{Clone: "alice", Name: "Alice (editor)"}
		}},
		{"names that differ beyond case", func(c *Config) {
			c.Participants["chair"] = Participant{Model: "default", Name: "Alice's chair"}
			c.Participants["editor"] = Participant{Model: "default", Name: "Bob's editor"}
		}},
		{"unused participant", func(c *Config) { c.Participants["spare"] = Participant{Model: "default"} }},
		{"json inline source", func(c *Config) {
			c.Sources["facts"] = Source{Decode: FormatJSON, Inline: cfgtRaw(`[1,{"a":null}]`)}
			c.Layers[0].Inputs = append(c.Layers[0].Inputs, Route{From: "source:facts", Paths: []string{"/a"}})
		}},
		{"json inline scalar", func(c *Config) { c.Sources["n"] = Source{Decode: FormatJSON, Inline: cfgtRaw(`null`)} }},
		{"relative file source", func(c *Config) { c.Sources["report"] = Source{Decode: FormatMarkdown, File: "docs/./report.md"} }},
		{"optional route from disabled layer", func(c *Config) { cfgtDisable(c, 1) }},
		{"disabled consumer may name a disabled producer", func(c *Config) {
			cfgtDisable(c, 0)
			cfgtDisable(c, 1) // debate requires review, but debate never runs
			c.Layers[2].Inputs[1].Optional = true
		}},
		{"layer max_calls equal to forum", func(c *Config) { c.Layers[1].MaxCalls = 30 }},
		{"layer max_calls absent", func(c *Config) { c.Layers[1].MaxCalls = 0 }},
		{"share on json output", func(c *Config) { c.Layers[0].Output.Share = cfgtStrs("/agreements") }},
		{"share empty publishes nothing", func(c *Config) { c.Layers[0].Output.Share = cfgtStrs() }},
		{"paths within share", func(c *Config) {
			c.Layers[0].Output.Share = cfgtStrs("/agreements")
			c.Layers[2].Inputs[1].Paths = []string{"/agreements"}
		}},
		{"paths under a shared member", func(c *Config) {
			c.Layers[0].Output.Share = cfgtStrs("/a")
			c.Layers[2].Inputs[1].Paths = []string{"/a/b"}
		}},
		{"paths enclosing a shared member", func(c *Config) {
			c.Layers[0].Output.Share = cfgtStrs("/a/b")
			c.Layers[2].Inputs[1].Paths = []string{"/a"}
		}},
		{"paths outside share with view full", func(c *Config) {
			c.Layers[0].Output.Share = cfgtStrs("/agreements")
			c.Layers[2].Inputs[1] = Route{From: "layer:review", View: ViewFull, To: []string{"editor"}, Paths: []string{"/disagreements"}}
		}},
		{"view full with to", func(c *Config) { c.Layers[1].Inputs[1].View = ViewFull; c.Layers[1].Inputs[1].To = []string{"alice"} }},
		{"one-to-one route", func(c *Config) {
			c.Layers[1].Inputs[1] = Route{From: "layer:review", Authors: []string{"bob"}, Select: SelectLastPerParticipant, To: []string{"alice"}}
		}},
		{"same_participant within the same set", func(c *Config) { c.Layers[1].Inputs[1].Distribute = DistributeSameParticipant }},
		{"same_participant to a subset", func(c *Config) {
			c.Layers[1].Inputs[1].Distribute = DistributeSameParticipant
			c.Layers[1].Inputs[1].To = []string{"bob"}
		}},
		{"random source to two, optional", func(c *Config) {
			c.Layers[0].Inputs[0].Distribute = DistributeRandom
			c.Layers[0].Inputs[0].Optional = true
		}},
		{"random source to one", func(c *Config) {
			c.Layers[0].Inputs[0].Distribute = DistributeRandom
			c.Layers[0].Inputs[0].To = []string{"bob"}
		}},
		{"random from a layer", func(c *Config) { c.Layers[1].Inputs[1].Distribute = DistributeRandom }},
		{"moderator options", func(c *Config) {
			c.Layers[1].Moderator.ConversationView = ConversationViewFull
			c.Layers[1].Moderator.Schema = "findings"
			c.Layers[1].Moderator.AllowDirected = true
			c.Layers[1].Moderator.AfterRound = 2
			c.Layers[1].Moderator.EveryRounds = 5
			c.Layers[1].Moderator.Inputs = append(c.Layers[1].Moderator.Inputs,
				Route{From: "layer:review", View: ViewFull, Select: SelectLastPerParticipant, Authors: []string{"alice"}, Distribute: DistributeAll})
		}},
		{"moderator also participates elsewhere", func(c *Config) { c.Layers[1].Moderator.Participant = "editor" }},
		{"no result_layers", func(c *Config) { c.ResultLayers = nil }},
		{"several result_layers", func(c *Config) { c.ResultLayers = []string{"review", "report"} }},
		{"no inputs", func(c *Config) { c.Layers[0].Inputs = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := cfgtExample(t)
			tt.mutate(cfg)
			if err := validateStatic(cfg); err != nil {
				t.Fatalf("ValidateStatic: %v", err)
			}
		})
	}
}

// TestValidateStaticProjection covers the share and paths pointer checks,
// which validateStatic delegates to checkProjection (jsonpointer.go).
func TestValidateStaticProjection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(c *Config)
		path   string
	}{
		{"share root pointer", func(c *Config) { c.Layers[0].Output.Share = cfgtStrs("") }, "layers[0].output.share"},
		{"share not a pointer", func(c *Config) { c.Layers[0].Output.Share = cfgtStrs("agreements") }, "layers[0].output.share"},
		{"share overlap", func(c *Config) { c.Layers[0].Output.Share = cfgtStrs("/a", "/a/b") }, "layers[0].output.share"},
		{"paths bad escape", func(c *Config) { c.Layers[2].Inputs[1].Paths = []string{"/a~2"} }, "layers[2].inputs[1].paths"},
		{"paths overlap", func(c *Config) { c.Layers[2].Inputs[1].Paths = []string{"/a", "/a"} }, "layers[2].inputs[1].paths"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := cfgtExample(t)
			tt.mutate(cfg)
			cfgtWantIssue(t, validateStatic(cfg), tt.path)
		})
	}
}

func TestValidateStaticReportsEveryIssue(t *testing.T) {
	cfg := cfgtExample(t)
	cfg.Brief.Task = ""
	cfg.Limits.MaxCalls = 0
	cfg.Layers[0].Delivery = ""
	cfg.Participants["alice"] = Participant{Agent: "alice", Model: "x"}
	cfg.ResultLayers = []string{"nope"}
	err := validateStatic(cfg)
	for _, path := range []string{"brief.task", "limits.max_calls", "layers[0].delivery", "participants.alice.model", "result_layers[0]"} {
		cfgtWantIssue(t, err, path)
	}
	// Issues come out in a stable order.
	again := validateStatic(cfg)
	if err.Error() != again.Error() {
		t.Errorf("issue order is not deterministic:\n%v\n---\n%v", err, again)
	}
}

func TestValidationErrorText(t *testing.T) {
	err := &ValidationError{Issues: []Issue{{Path: "brief.task", Message: "is required"}, {Message: "malformed JSON"}}}
	want := "invalid configuration:\nbrief.task: is required\nmalformed JSON"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	var target *ValidationError
	if !errors.As(error(err), &target) || !strings.Contains(target.Error(), "brief.task") {
		t.Error("errors.As")
	}
}

func TestWithinShare(t *testing.T) {
	tests := []struct {
		p     string
		share []string
		want  bool
	}{
		{"/a", []string{"/a"}, true},
		{"/a/b", []string{"/a"}, true},
		{"/a", []string{"/a/b"}, true},
		{"/ab", []string{"/a"}, false},
		{"/b", []string{"/a", "/c"}, false},
		{"/b", []string{"/a", "/b/c"}, true},
		{"/a", nil, false},
		{"bad", []string{"/a"}, true},    // left to checkProjection
		{"/a~1b", []string{"/a"}, false}, // the member "a/b", not "a" then "b"
		{"/a/b", []string{"/a~1b"}, false},
		{"/a~1b", []string{"/a~1b"}, true},
		{"/a~0/x", []string{"/a~0"}, true},
		{"/a", []string{"bad"}, true}, // left to checkProjection
	}
	for _, tt := range tests {
		if got := withinShare(tt.p, tt.share); got != tt.want {
			t.Errorf("withinShare(%q, %q) = %v, want %v", tt.p, tt.share, got, tt.want)
		}
	}
}

// Any string of the configuration that is entirely a "<...>" placeholder is
// refused with its path; text that only contains angle brackets is not.
func TestValidateStaticPlaceholders(t *testing.T) {
	refused := []struct {
		name   string
		mutate func(c *Config)
		path   string
	}{
		{"layer instructions", func(c *Config) { c.Layers[2].Instructions = "<x>" }, "layers[2].instructions"},
		{"padded success criterion", func(c *Config) { c.Brief.SuccessCriteria = []string{"ok", " \t<the criterion>\n"} }, "brief.success_criteria[1]"},
		{"brief purpose", func(c *Config) { c.Brief.Purpose = "<purpose>" }, "brief.purpose"},
		{"forum name", func(c *Config) { c.Name = "<name>" }, "name"},
		{"participant model", func(c *Config) { c.Participants["chair"] = Participant{Model: "<a model from forum_models>"} }, "participants.chair.model"},
		{"participant name", func(c *Config) { c.Participants["chair"] = Participant{Model: "default", Name: "<name>"} }, "participants.chair.name"},
		{"source file", func(c *Config) { c.Sources["report"] = Source{Decode: FormatText, File: "<path of the topic>"} }, "sources.report.file"},
		{"inline text", func(c *Config) {
			c.Sources["report"] = Source{Decode: FormatMarkdown, Inline: cfgtRaw(`"<the question>"`)}
		}, "sources.report.inline"},
		{"string inside inline json", func(c *Config) {
			c.Sources["report"] = Source{Decode: FormatJSON, Inline: cfgtRaw(`{"items":[1,"<item>"]}`)}
		}, "sources.report.inline.items[1]"},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			cfg := cfgtExample(t)
			tt.mutate(cfg)
			cfgtWantIssue(t, validateStatic(cfg), tt.path, "is still a placeholder; replace it.")
		})
	}

	// Every placeholder is listed at once.
	cfg := cfgtExample(t)
	cfg.Brief.Task = "<task>"
	cfg.Layers[0].Instructions = "<x>"
	err := validateStatic(cfg)
	cfgtWantIssue(t, err, "brief.task", "placeholder")
	cfgtWantIssue(t, err, "layers[0].instructions", "placeholder")

	// A schema's strings are real data: a pattern "<\w+>" is accepted.
	cfg = cfgtExample(t)
	cfg.Schemas["tag"] = cfgtRaw(`{"type":"string","pattern":"<\\w+>"}`)
	if err := validateStatic(cfg); err != nil {
		t.Errorf("a schema pattern is refused: %v", err)
	}

	for _, text := range []string{"Is a<b?", "<b>bold</b> text", "<>", "<a<b>", "<a>b>", "see <x>", "<x> first", "x"} {
		cfg := cfgtExample(t)
		cfg.Layers[2].Instructions = text
		cfg.Sources["report"] = Source{Decode: FormatMarkdown, Inline: cfgtRaw(`"` + text + `"`)}
		if err := validateStatic(cfg); err != nil {
			t.Errorf("%q is refused: %v", text, err)
		}
	}
}
