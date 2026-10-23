package tools

import "maps"

// OpenObjectProperties returns schema with "additionalProperties": true on
// every object-typed property (at any depth under "properties") that
// declares neither "properties" nor "additionalProperties": a free-form
// object argument such as a JSON merge patch. JSON Schema already allows
// any member there, but some model upstreams fill an object parameter that
// says nothing about its members with a blank string instead of an object;
// saying so explicitly makes them send the object. The input is never
// modified: what changes is copied.
func OpenObjectProperties(schema map[string]any) map[string]any {
	out, _ := openProperties(schema)
	return out
}

// openProperties is OpenObjectProperties, reporting whether it changed
// anything.
func openProperties(schema map[string]any) (map[string]any, bool) {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return schema, false
	}
	var opened map[string]any
	for name, p := range props {
		prop, ok := p.(map[string]any)
		if !ok {
			continue
		}
		next, changed := openObject(prop)
		if !changed {
			continue
		}
		if opened == nil {
			opened = copyMap(props)
		}
		opened[name] = next
	}
	if opened == nil {
		return schema, false
	}
	out := copyMap(schema)
	out["properties"] = opened
	return out, true
}

// openObject applies the rule to one property schema.
func openObject(prop map[string]any) (map[string]any, bool) {
	if prop["type"] != "object" {
		return prop, false
	}
	if _, has := prop["properties"]; has {
		return openProperties(prop)
	}
	if _, has := prop["additionalProperties"]; has {
		return prop, false
	}
	out := copyMap(prop)
	out["additionalProperties"] = true
	return out, true
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+1)
	maps.Copy(out, m)
	return out
}
