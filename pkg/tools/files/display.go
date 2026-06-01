package files

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

// displayHeader builds an action-verb header line like "**Wrote:** <path>"
// for the display block.
func displayHeader(verb, path string) string {
	if path == "" {
		return ""
	}
	return "**" + verb + ":** " + path
}

// displayBody wraps a payload in the `---` fenced block used by tools that
// expose an optional `display` parameter.
func displayBody(header, payload string) string {
	if header == "" {
		return "---\n" + payload + "\n---"
	}
	return "---\n" + header + "\n---\n\n" + payload + "\n---"
}
