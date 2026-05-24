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

// displayHeader builds an action-verb header line like "Wrote: <path>" for
// the display block. Returns an empty string when path is empty so the
// caller emits no header line at all.
func displayHeader(verb, path string) string {
	if path == "" {
		return ""
	}
	return verb + ": " + path
}

// displayBody wraps a payload in the `---` fenced block used by tools that
// expose an optional `display` parameter. When header is non-empty it is
// emitted as the first line inside the block, followed by a blank line and
// then the payload.
func displayBody(header, payload string) string {
	if header == "" {
		return "---\n" + payload + "\n---"
	}
	return "---\n" + header + "\n\n" + payload + "\n---"
}
