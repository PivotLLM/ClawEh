package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzLoadConfig writes fuzzed bytes to a config file and loads it. LoadConfig
// must never panic and must return exactly one of a Config or an error; a
// returned Config must be encodable, since the WebUI saves it back to disk.
func FuzzLoadConfig(f *testing.F) {
	def, err := json.Marshal(DefaultConfig())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(def)
	for _, s := range []string{
		`{}`,
		``,
		`null`,
		`[]`,
		`"string"`,
		`{"models":[{}]}`,
		`{"models":[{"model_name":"m","model":"x","provider":"p","enabled":true}],"providers":[{"name":"p","protocol":"openai-chat","base_url":"http://h","api_key":"k"}]}`,
		`{"providers":[{"name":"x"}],"agents":{"list":[{"id":"a","workspace":"./w"}],"defaults":{"model":"m"}}}`,
		`{"agents":{"defaults":{"compress_model":"legacy"},"list":[{"compress_model":"legacy"}]}}`,
		`{"channels":{"telegram_bots":[{"id":"a","token":"t"}],"line":{"enabled":true,"channel_secret":"s"}}}`,
		`{"channels":{"device":{"enabled":true,"port":18791,"allow_origins":["*"]}}}`,
		`{"gateway":{"port":"not-a-number"}}`,
		`{"gateway":{"port":-1,"allowed_cidrs":["10.0.0.0/8","garbage"]}}`,
		`{"tools":{"exec":{"allow_remote":true},"web":{"proxy":"http://127.0.0.1:7890"}}}`,
		`{"agents":{"list":[{"allow_from":"single"}]},"channels":{"line":{"allow_from":["a","b"]}}}`,
		`{"summarization":{"models":["a"]},"maestro":{"enabled":"yes"}}`,
		strings.Repeat("[", 5000) + strings.Repeat("]", 5000),
		strings.Repeat(`{"a":`, 2000) + `1` + strings.Repeat("}", 2000),
		`{"a":1e400}`,
		"{\"gateway\":{\"host\":\"\xff\xfe\"}}",
		`{"models":[null,null],"providers":[null],"agents":{"list":[null]}}`,
	} {
		f.Add([]byte(s))
	}

	dir := f.TempDir()
	path := filepath.Join(dir, "config.json")

	f.Fuzz(func(t *testing.T, data []byte) {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(path)
		if (cfg == nil) == (err == nil) {
			t.Fatalf("LoadConfig(%q) = (%v, %v): want exactly one of config or error", data, cfg, err)
		}
		if err != nil {
			return
		}
		if _, merr := json.Marshal(cfg); merr != nil {
			t.Fatalf("LoadConfig(%q) returned a config that does not encode: %v", data, merr)
		}
	})
}
