// ClawEh
// License: MIT

package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCronStore(t *testing.T, dataDir, body string) string {
	t.Helper()
	dir := filepath.Join(dataDir, "cron")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "jobs.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCollectScheduled_NoStore(t *testing.T) {
	cfg, env := fixtureConfig(t)
	tb := findTable(t, collectScheduled(t.Context(), cfg, env), "Cron jobs")
	contains(t, tb.Rows[0][0], "(none: no cron store at", "missing cron store")
}

func TestCollectScheduled_JobsListed(t *testing.T) {
	cfg, env := fixtureConfig(t)
	long := strings.Repeat("check the mail and report anything new ", 4)
	writeCronStore(t, env.DataDir, `{"version":1,"jobs":[
		{"id":"j1","name":"Mail check","agentId":"bob","enabled":true,
		 "schedule":{"kind":"cron","expr":"0 9 * * *","tz":"America/Toronto"},
		 "payload":{"mode":"agent","message":"`+long+`","channel":"telegram-bob","to":"42","peer_kind":"direct"}},
		{"id":"j2","name":"Ping","enabled":false,
		 "schedule":{"kind":"every","everyMs":300000},
		 "payload":{"mode":"command","command":"echo hi"}}
	]}`)
	tb := findTable(t, collectScheduled(t.Context(), cfg, env), "Cron jobs")
	_, mail := findRow(t, tb, "Mail check")
	if mail[1] != "0 9 * * * (America/Toronto)" || mail[2] != "bob" || mail[3] != "yes" {
		t.Errorf("mail row = %v", mail)
	}
	if !strings.HasPrefix(mail[4], "agent: check the mail") || !strings.HasSuffix(mail[4], "...") {
		t.Errorf("payload = %q", mail[4])
	}
	if len(mail[4]) > len("agent: ")+payloadPreview+3 {
		t.Errorf("payload not truncated to %d chars: %d", payloadPreview, len(mail[4]))
	}
	if mail[5] != "telegram-bob 42 (direct)" {
		t.Errorf("delivery = %q", mail[5])
	}
	_, ping := findRow(t, tb, "Ping")
	if ping[1] != "every 300s" || ping[2] != "(operator)" || ping[3] != "no" || ping[4] != "command: echo hi" {
		t.Errorf("ping row = %v", ping)
	}
}

func TestCollectScheduled_CorruptStoreIsARow(t *testing.T) {
	cfg, env := fixtureConfig(t)
	writeCronStore(t, env.DataDir, "{oops")
	tb := findTable(t, collectScheduled(t.Context(), cfg, env), "Cron jobs")
	contains(t, tb.Rows[0][0], "unavailable:", "corrupt cron store")
}

func TestCollectScheduled_Maestro(t *testing.T) {
	cfg, env := fixtureConfig(t)
	tb := findTable(t, collectScheduled(t.Context(), cfg, env), "Maestro task orchestration")
	_, bob := findRow(t, tb, "bob")
	if bob[1] != "yes" || bob[2] != "3" || bob[3] != "allowed when requested" {
		t.Errorf("bob maestro row = %v", bob)
	}
	_, alice := findRow(t, tb, "alice")
	if alice[1] != "no" {
		t.Errorf("alice maestro row = %v", alice)
	}
}
