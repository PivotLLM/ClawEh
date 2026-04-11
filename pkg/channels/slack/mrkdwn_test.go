package slack

import (
	"testing"
)

func TestMarkdownToMrkdwn(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// Bold
		{
			name:  "bold",
			input: "**bold text**",
			want:  "*bold text*",
		},
		{
			name:  "bold in sentence",
			input: "This is **important** info",
			want:  "This is *important* info",
		},
		// Italic
		{
			name:  "italic",
			input: "*italic text*",
			want:  "_italic text_",
		},
		{
			name:  "italic in sentence",
			input: "This is *emphasised* here",
			want:  "This is _emphasised_ here",
		},
		// Bold is not double-converted to italic
		{
			name:  "bold not double converted",
			input: "**bold** and *italic*",
			want:  "*bold* and _italic_",
		},
		// Strikethrough
		{
			name:  "strikethrough",
			input: "~~struck~~",
			want:  "~struck~",
		},
		{
			name:  "strikethrough in sentence",
			input: "This is ~~deleted~~ text",
			want:  "This is ~deleted~ text",
		},
		// Links
		{
			name:  "markdown link",
			input: "[OpenAI](https://openai.com)",
			want:  "<https://openai.com|OpenAI>",
		},
		{
			name:  "link in sentence",
			input: "Visit [our site](https://example.com) for more",
			want:  "Visit <https://example.com|our site> for more",
		},
		// Headers
		{
			name:  "h1 header",
			input: "# Heading One",
			want:  "*Heading One*",
		},
		{
			name:  "h2 header",
			input: "## Heading Two",
			want:  "*Heading Two*",
		},
		{
			name:  "h6 header",
			input: "###### Deep Header",
			want:  "*Deep Header*",
		},
		{
			name:  "header in multiline text",
			input: "## Title\nSome body text.",
			want:  "*Title*\nSome body text.",
		},
		// Horizontal rules
		{
			name:  "horizontal rule dashes",
			input: "Above\n---\nBelow",
			want:  "Above\n\nBelow",
		},
		{
			name:  "horizontal rule asterisks",
			input: "Above\n***\nBelow",
			want:  "Above\n\nBelow",
		},
		// Code spans preserved
		{
			name:  "inline code preserved",
			input: "Use `fmt.Println` to print",
			want:  "Use `fmt.Println` to print",
		},
		{
			name:  "inline code with bold inside preserved",
			input: "Run `**not bold**` here",
			want:  "Run `**not bold**` here",
		},
		// Fenced code blocks preserved
		{
			name:  "fenced code block preserved",
			input: "```\n**not bold**\n*not italic*\n```",
			want:  "```\n**not bold**\n*not italic*\n```",
		},
		// Blockquotes unchanged
		{
			name:  "blockquote unchanged",
			input: "> This is a quote",
			want:  "> This is a quote",
		},
		// Bullet lists unchanged
		{
			name:  "bullet list unchanged",
			input: "- item one\n- item two",
			want:  "- item one\n- item two",
		},
		// Numbered lists unchanged
		{
			name:  "numbered list unchanged",
			input: "1. First\n2. Second",
			want:  "1. First\n2. Second",
		},
		// Plain text unchanged
		{
			name:  "plain text unchanged",
			input: "Hello world",
			want:  "Hello world",
		},
		// Empty string
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		// Combined conversions
		{
			name:  "combined bold italic link",
			input: "**Bold** and *italic* with [link](https://example.com)",
			want:  "*Bold* and _italic_ with <https://example.com|link>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := markdownToMrkdwn(tt.input)
			if got != tt.want {
				t.Errorf("markdownToMrkdwn(%q)\n  got:  %q\n  want: %q", tt.input, got, tt.want)
			}
		})
	}
}
