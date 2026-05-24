package tools

// getBoolArg extracts a boolean argument from the args map, returning the
// provided default when the key is absent or the value is not a bool.
func getBoolArg(args map[string]any, key string, def bool) bool {
	raw, ok := args[key]
	if !ok {
		return def
	}
	if b, ok := raw.(bool); ok {
		return b
	}
	return def
}

// displayBody wraps a payload in the `---` fenced block used by tools that
// expose an optional `display` parameter.
func displayBody(payload string) string {
	return "---\n" + payload + "\n---"
}
