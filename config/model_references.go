package config

import (
	"fmt"
	"slices"
)

// modelRef is one place a model alias is referenced from. Exactly one of
// slice and scalar is set.
type modelRef struct {
	where  string    // human label, e.g. `agents.list[alice].models`
	slice  *[]string // set for list sites
	scalar *string   // set for scalar sites
}

// DanglingModelReference is one reference PruneDanglingModelReferences removed:
// the labelled site it lived at and the alias it named.
type DanglingModelReference struct {
	Site  string
	Alias string
}

// modelRefSites visits every site that references a model alias exactly once,
// so rename, remove and validate cannot drift apart. Pointers are into c.
func (c *Config) modelRefSites() []modelRef {
	d := &c.Agents.Defaults
	sites := []modelRef{
		{where: "agents.defaults.models", slice: &d.Models},
		{where: "agents.defaults.image_model", scalar: &d.ImageModel},
		{where: "agents.defaults.image_model_fallbacks", slice: &d.ImageModelFallbacks},
		{where: "agents.defaults.vision_model", scalar: &d.VisionModel},
		{where: "agents.defaults.vision_model_fallbacks", slice: &d.VisionModelFallbacks},
		{where: "summarization.models", slice: &c.Summarization.Models},
	}
	for i := range c.Agents.List {
		a := &c.Agents.List[i]
		sites = append(sites,
			modelRef{where: fmt.Sprintf("agents.list[%s].models", a.ID), slice: &a.Models},
			modelRef{where: fmt.Sprintf("agents.list[%s].summarization_models", a.ID), slice: &a.SummarizationModels},
		)
		if a.Subagents != nil {
			sites = append(sites, modelRef{where: fmt.Sprintf("agents.list[%s].subagents.models", a.ID), slice: &a.Subagents.Models})
		}
	}
	return sites
}

// values returns the aliases held at the site, empty entries included.
func (r modelRef) values() []string {
	if r.slice != nil {
		return *r.slice
	}
	return []string{*r.scalar}
}

// ModelReferences returns the labelled sites that reference alias.
func (c *Config) ModelReferences(alias string) []string {
	var refs []string
	for _, site := range c.modelRefSites() {
		if slices.Contains(site.values(), alias) {
			refs = append(refs, site.where)
		}
	}
	return refs
}

// modelNameSets reports which aliases exist in Models and which have at least
// one enabled entry. Duplicate aliases are legal (load balancing), so "exists"
// means at least one entry with that model_name.
func (c *Config) modelNameSets() (exists, enabled map[string]bool) {
	exists = make(map[string]bool, len(c.Models))
	enabled = make(map[string]bool, len(c.Models))
	for i := range c.Models {
		exists[c.Models[i].ModelName] = true
		if c.Models[i].Enabled {
			enabled[c.Models[i].ModelName] = true
		}
	}
	return exists, enabled
}

// ValidateModelReferences reports every reference to an alias that is not an
// enabled model. Missing aliases are returned as errors; references to a model
// that exists but is disabled are returned separately as warnings. Empty
// entries are skipped (the resolver skips them too).
func (c *Config) ValidateModelReferences() (errs []error, warnings []string) {
	exists, enabled := c.modelNameSets()
	for _, site := range c.modelRefSites() {
		for _, v := range site.values() {
			switch {
			case v == "":
			case !exists[v]:
				errs = append(errs, fmt.Errorf("%s: model %q does not exist", site.where, v))
			case !enabled[v]:
				warnings = append(warnings, fmt.Sprintf("%s: model %q is disabled", site.where, v))
			}
		}
	}
	return errs, warnings
}

// PruneDanglingModelReferences removes every reference to an alias that does
// not exist in Models (disabled models are left alone) and returns what it
// removed. Scalars are blanked; list entries are deleted, not blanked. It
// mutates the in-memory config only — it never writes to disk.
func (c *Config) PruneDanglingModelReferences() []DanglingModelReference {
	exists, _ := c.modelNameSets()
	var removed []DanglingModelReference
	for _, site := range c.modelRefSites() {
		if site.scalar != nil {
			if v := *site.scalar; v != "" && !exists[v] {
				removed = append(removed, DanglingModelReference{Site: site.where, Alias: v})
				*site.scalar = ""
			}
			continue
		}
		kept := (*site.slice)[:0]
		for _, v := range *site.slice {
			if v != "" && !exists[v] {
				removed = append(removed, DanglingModelReference{Site: site.where, Alias: v})
				continue
			}
			kept = append(kept, v)
		}
		*site.slice = kept
	}
	return removed
}
