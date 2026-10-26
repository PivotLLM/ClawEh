package workspace

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/templates"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func channelTemplate(t *testing.T, name string) string {
	t.Helper()
	data, err := templates.FS.ReadFile(channelTemplatesDir + "/" + name)
	if err != nil {
		t.Fatalf("read template %s: %v", name, err)
	}
	return string(data)
}

// kindOf resolves channel names without a config.
var kindOf = (*config.Config)(nil).ChannelKind

func TestPopulate_ChannelFiles(t *testing.T) {
	tests := []struct {
		name     string
		kinds    []string
		existing map[string]string // file -> content before Populate
		seeded   []string          // files holding their template after Populate
		kept     map[string]string // files holding exactly this content after
		absent   []string
	}{
		{
			name:   "in-use kinds seeded, others not created",
			kinds:  []string{config.ChannelKindDevice, config.ChannelKindTelegram},
			seeded: []string{"DEVICE.md", "TELEGRAM.md"},
			absent: []string{"SLACK.md", "DISCORD.md", "MATRIX.md", "SECMSG.md", "WEBUI.md"},
		},
		{
			name:   "no kinds in use",
			absent: []string{"DEVICE.md", "TELEGRAM.md", "SLACK.md", "DISCORD.md", "MATRIX.md", "SECMSG.md", "WEBUI.md"},
		},
		{
			name:     "existing in-use file untouched",
			kinds:    []string{config.ChannelKindDevice},
			existing: map[string]string{"DEVICE.md": "Alice's device text."},
			kept:     map[string]string{"DEVICE.md": "Alice's device text."},
		},
		{
			name:     "existing not-in-use file untouched",
			kinds:    []string{config.ChannelKindDevice},
			existing: map[string]string{"SLACK.md": "Bob's Slack text."},
			seeded:   []string{"DEVICE.md"},
			kept:     map[string]string{"SLACK.md": "Bob's Slack text."},
		},
		{
			name:     "emptied file kept empty",
			kinds:    []string{config.ChannelKindWebUI},
			existing: map[string]string{"WEBUI.md": ""},
			kept:     map[string]string{"WEBUI.md": ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := t.TempDir()
			for f, c := range tt.existing {
				writeFile(t, filepath.Join(ws, f), c)
			}
			Populate(ws, 0o700, 0o600, tt.kinds, kindOf)
			for _, f := range tt.seeded {
				if got, want := readFile(t, filepath.Join(ws, f)), channelTemplate(t, f); got != want {
					t.Errorf("%s = %q, want the template %q", f, got, want)
				}
			}
			for f, want := range tt.kept {
				if got := readFile(t, filepath.Join(ws, f)); got != want {
					t.Errorf("%s = %q, want %q", f, got, want)
				}
			}
			for _, f := range tt.absent {
				if exists(t, filepath.Join(ws, f)) {
					t.Errorf("%s must not be created", f)
				}
			}
		})
	}
}

// A deleted file of an in-use kind returns with the current default.
func TestPopulate_ChannelFileRecreated(t *testing.T) {
	ws := t.TempDir()
	kinds := []string{config.ChannelKindDevice}
	Populate(ws, 0o700, 0o600, kinds, kindOf)
	path := filepath.Join(ws, "DEVICE.md")
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	Populate(ws, 0o700, 0o600, kinds, kindOf) // restart: initialized workspace
	if got, want := readFile(t, path), channelTemplate(t, "DEVICE.md"); got != want {
		t.Errorf("recreated DEVICE.md = %q, want the template %q", got, want)
	}
}

// The generic walk does not copy templates/channels into the workspace.
func TestPopulate_ChannelTemplatesNotWalked(t *testing.T) {
	ws := t.TempDir()
	Populate(ws, 0o700, 0o600, nil, nil)
	if exists(t, filepath.Join(ws, channelTemplatesDir)) {
		t.Errorf("%s/ must not be created in the workspace", channelTemplatesDir)
	}
}

func TestPopulate_MigratesOldChannelFiles(t *testing.T) {
	tests := []struct {
		name     string
		kinds    []string
		existing map[string]string
		want     map[string]string
		absent   []string
	}{
		{
			name:     "renamed to the kind's file",
			kinds:    []string{config.ChannelKindDevice},
			existing: map[string]string{"channel-device.md": "old device text"},
			want:     map[string]string{"DEVICE.md": "old device text"},
			absent:   []string{"channel-device.md"},
		},
		{
			name:     "renamed when the kind is not in use",
			existing: map[string]string{"channel-slack.md": "old slack text"},
			want:     map[string]string{"SLACK.md": "old slack text"},
			absent:   []string{"channel-slack.md"},
		},
		{
			name:     "telegram bot file renamed",
			kinds:    []string{config.ChannelKindTelegram},
			existing: map[string]string{"channel-telegram-main.md": "old bot text"},
			want:     map[string]string{"TELEGRAM.md": "old bot text"},
			absent:   []string{"channel-telegram-main.md"},
		},
		{
			name:  "target exists: old file left in place",
			kinds: []string{config.ChannelKindTelegram},
			existing: map[string]string{
				"TELEGRAM.md":              "current text",
				"channel-telegram-main.md": "old bot text",
			},
			want: map[string]string{
				"TELEGRAM.md":              "current text",
				"channel-telegram-main.md": "old bot text",
			},
		},
		{
			name:  "second file of the same kind left in place",
			kinds: []string{config.ChannelKindTelegram},
			existing: map[string]string{
				"channel-telegram-alice.md": "alice text",
				"channel-telegram-bob.md":   "bob text",
			},
			want: map[string]string{
				"TELEGRAM.md":             "alice text", // first in name order
				"channel-telegram-bob.md": "bob text",
			},
			absent: []string{"channel-telegram-alice.md"},
		},
		{
			name:     "unknown channel left alone",
			existing: map[string]string{"channel-agent_message.md": "internal"},
			want:     map[string]string{"channel-agent_message.md": "internal"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := t.TempDir()
			for f, c := range tt.existing {
				writeFile(t, filepath.Join(ws, f), c)
			}
			Populate(ws, 0o700, 0o600, tt.kinds, kindOf)
			for f, c := range tt.want {
				if got := readFile(t, filepath.Join(ws, f)); got != c {
					t.Errorf("%s = %q, want %q", f, got, c)
				}
			}
			for _, f := range tt.absent {
				if exists(t, filepath.Join(ws, f)) {
					t.Errorf("%s must be gone", f)
				}
			}
		})
	}
}

// Every channel kind has a template, and no channel template shares a name
// with another seeded workspace file.
func TestChannelTemplates(t *testing.T) {
	top := map[string]bool{}
	entries, err := fs.ReadDir(templates.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			top[e.Name()] = true
		}
	}
	if len(top) == 0 {
		t.Fatal("no top-level templates found")
	}

	channelFiles, err := fs.ReadDir(templates.FS, channelTemplatesDir)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, e := range channelFiles {
		have[e.Name()] = true
		if top[e.Name()] {
			t.Errorf("channel template %s collides with a workspace template", e.Name())
		}
	}
	for _, kind := range config.ChannelKinds() {
		name := config.ChannelPromptFile(kind)
		if !have[name] {
			t.Errorf("channel kind %q has no template %s/%s", kind, channelTemplatesDir, name)
		}
		delete(have, name)
	}
	for name := range have {
		t.Errorf("template %s/%s belongs to no channel kind", channelTemplatesDir, name)
	}
}
