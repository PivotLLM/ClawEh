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

// displayHeader builds an action-verb header line like "**Wrote:** <path>"
// for the display block. The verb prefix (including the colon) is wrapped in
// bold markers; the path is left unbolded. Returns an empty string when path
// is empty so the caller emits no header line at all.
func displayHeader(verb, path string) string {
	if path == "" {
		return ""
	}
	return "**" + verb + ":** " + path
}

// displayBody wraps a payload in the `---` fenced block used by tools that
// expose an optional `display` parameter. When header is non-empty it is
// emitted as the first line inside the block, followed by a separator rule
// (the same `---` glyph as the outer fences so downstream HR-collapse logic
// continues to dedupe adjacent rules), a blank line, and then the payload.
func displayBody(header, payload string) string {
	if header == "" {
		return "---\n" + payload + "\n---"
	}
	return "---\n" + header + "\n---\n\n" + payload + "\n---"
}
