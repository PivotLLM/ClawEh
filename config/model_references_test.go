package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// refTestConfig references "alpha" from every site the walker knows, plus
// "beta" (disabled) and "ghost" (missing) where the test needs them.
func refTestConfig() *Config {
	cfg := &Config{}
	cfg.Models = []ModelConfig{
		{ModelName: "alpha", Model: "a", Provider: "p", Enabled: true},
		{ModelName: "beta", Model: "b", Provider: "p", Enabled: false},
		{ModelName: "alpha", Model: "a2", Provider: "p2", Enabled: false}, // duplicate alias: legal
	}
	cfg.Agents.Defaults.Models = []string{"alpha"}
	cfg.Agents.Defaults.ImageModel = "alpha"
	cfg.Agents.Defaults.ImageModelFallbacks = []string{"alpha"}
	cfg.Agents.Defaults.VisionModel = "alpha"
	cfg.Agents.Defaults.VisionModelFallbacks = []string{"alpha"}
	cfg.Summarization.Models = []string{"alpha"}
	cfg.Agents.List = []AgentConfig{{
		ID:                  "alice",
		Models:              []string{"alpha"},
		SummarizationModels: []string{"alpha"},
		Subagents:           &SubagentsConfig{Models: []string{"alpha"}},
	}, {
		ID: "bob", // no Subagents: that site must not be listed
	}}
	return cfg
}

var allSites = []string{
	"agents.defaults.models",
	"agents.defaults.image_model",
	"agents.defaults.image_model_fallbacks",
	"agents.defaults.vision_model",
	"agents.defaults.vision_model_fallbacks",
	"summarization.models",
	"agents.list[alice].models",
	"agents.list[alice].summarization_models",
	"agents.list[alice].subagents.models",
}

func TestModelReferences_LabelsEverySite(t *testing.T) {
	cfg := refTestConfig()
	if got := cfg.ModelReferences("alpha"); !reflect.DeepEqual(got, allSites) {
		t.Fatalf("ModelReferences(alpha) = %v\nwant %v", got, allSites)
	}
	if got := cfg.ModelReferences("nope"); len(got) != 0 {
		t.Fatalf("ModelReferences(nope) = %v, want none", got)
	}
}

func TestRenameModelReferences_CoversSubagentsModels(t *testing.T) {
	cfg := refTestConfig()
	cfg.RenameModelReferences("alpha", "omega")

	if got := cfg.ModelReferences("alpha"); len(got) != 0 {
		t.Fatalf("old alias still referenced at %v", got)
	}
	if got := cfg.ModelReferences("omega"); !reflect.DeepEqual(got, allSites) {
		t.Fatalf("ModelReferences(omega) = %v\nwant %v", got, allSites)
	}
	if got := cfg.Agents.List[0].Subagents.Models; !reflect.DeepEqual(got, []string{"omega"}) {
		t.Fatalf("subagents.models = %v, want [omega]", got)
	}
}

func TestValidateModelReferences(t *testing.T) {
	tests := []struct {
		name         string
		mutate       func(*Config)
		wantErrs     []string
		wantWarnings []string
	}{
		{
			name:   "valid config reports nothing",
			mutate: func(*Config) {},
		},
		{
			name: "missing alias is an error naming site and alias",
			mutate: func(c *Config) {
				c.Agents.List[0].Models = []string{"ghost", "alpha"}
				c.Agents.Defaults.ImageModel = "ghost"
			},
			wantErrs: []string{
				`agents.defaults.image_model: model "ghost" does not exist`,
				`agents.list[alice].models: model "ghost" does not exist`,
			},
		},
		{
			name: "disabled alias is a warning",
			mutate: func(c *Config) {
				c.Summarization.Models = []string{"beta"}
			},
			wantWarnings: []string{`summarization.models: model "beta" is disabled`},
		},
		{
			name: "empty entries are skipped",
			mutate: func(c *Config) {
				c.Agents.Defaults.Models = []string{"", "alpha"}
				c.Agents.Defaults.VisionModel = ""
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := refTestConfig()
			tt.mutate(cfg)
			errs, warnings := cfg.ValidateModelReferences()
			gotErrs := make([]string, 0, len(errs))
			for _, e := range errs {
				gotErrs = append(gotErrs, e.Error())
			}
			if !slices.Equal(gotErrs, tt.wantErrs) {
				t.Errorf("errs = %v, want %v", gotErrs, tt.wantErrs)
			}
			if !reflect.DeepEqual(warnings, tt.wantWarnings) {
				t.Errorf("warnings = %v, want %v", warnings, tt.wantWarnings)
			}
		})
	}
}

func TestPruneDanglingModelReferences(t *testing.T) {
	cfg := refTestConfig()
	cfg.Agents.List[0].Models = []string{"ghost", "alpha", "beta", "phantom"}
	cfg.Agents.List[0].Subagents.Models = []string{"ghost"}
	cfg.Agents.Defaults.ImageModel = "ghost"
	cfg.Agents.Defaults.VisionModel = "beta"

	removed := cfg.PruneDanglingModelReferences()

	want := []DanglingModelReference{
		{Site: "agents.defaults.image_model", Alias: "ghost"},
		{Site: "agents.list[alice].models", Alias: "ghost"},
		{Site: "agents.list[alice].models", Alias: "phantom"},
		{Site: "agents.list[alice].subagents.models", Alias: "ghost"},
	}
	if !reflect.DeepEqual(removed, want) {
		t.Fatalf("removed = %v\nwant %v", removed, want)
	}
	// List entries are deleted, not blanked; the disabled reference stays.
	if got := cfg.Agents.List[0].Models; !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
		t.Fatalf("agents.list[alice].models = %v, want [alpha beta]", got)
	}
	if got := cfg.Agents.List[0].Subagents.Models; len(got) != 0 {
		t.Fatalf("subagents.models = %v, want empty", got)
	}
	// Scalars are blanked; a disabled scalar is left alone.
	if cfg.Agents.Defaults.ImageModel != "" {
		t.Fatalf("image_model = %q, want blank", cfg.Agents.Defaults.ImageModel)
	}
	if cfg.Agents.Defaults.VisionModel != "beta" {
		t.Fatalf("vision_model = %q, want beta (disabled models are kept)", cfg.Agents.Defaults.VisionModel)
	}
	// Nothing dangling remains, and a second pass removes nothing.
	if errs, _ := cfg.ValidateModelReferences(); len(errs) != 0 {
		t.Fatalf("dangling references remain: %v", errs)
	}
	if again := cfg.PruneDanglingModelReferences(); len(again) != 0 {
		t.Fatalf("second prune removed %v, want nothing", again)
	}
}

func TestSetDefaultModel(t *testing.T) {
	tests := []struct {
		name   string
		before []string
		set    string
		want   []string
	}{
		{"empty name removes slot 0 and keeps order", []string{"a", "b", "c"}, "", []string{"b", "c"}},
		{"empty name on empty list is a no-op", nil, "", nil},
		{"empty name on single entry empties the list", []string{"a"}, "", []string{}},
		{"replaces slot 0", []string{"a", "b"}, "z", []string{"z", "b"}},
		{"creates the list", nil, "z", []string{"z"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := AgentDefaults{Models: tt.before}
			d.SetDefaultModel(tt.set)
			if strings.Join(d.Models, ",") != strings.Join(tt.want, ",") || len(d.Models) != len(tt.want) {
				t.Fatalf("Models = %v, want %v", d.Models, tt.want)
			}
			for _, m := range d.Models {
				if m == "" {
					t.Fatalf("Models = %v holds a blank slot", d.Models)
				}
			}
		})
	}
}
