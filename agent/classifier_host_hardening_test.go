// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/classifier"
	"github.com/PivotLLM/ClawEh/config"
)

// Before Bind every operation is refused as not ready, and the modes are
// the secure defaults.
func TestClassifierHost_BeforeBind(t *testing.T) {
	h := NewClassifierHost()
	ctx := context.Background()
	if _, err := h.Call(ctx, "alice", "inbox_list", nil); !errors.Is(err, errLoopNotReady) {
		t.Errorf("Call = %v", err)
	}
	if _, _, err := h.Complete(ctx, "alice", "s", "u"); !errors.Is(err, errLoopNotReady) {
		t.Errorf("Complete = %v", err)
	}
	if err := h.Summary(ctx, "alice", "text"); !errors.Is(err, errLoopNotReady) {
		t.Errorf("Summary = %v", err)
	}
	if dir, file := h.Modes("/x"); dir != config.DefaultDirMode || file != config.DefaultFileMode {
		t.Errorf("Modes = %o, %o", dir, file)
	}
	// A classifier tool is refused before anything else, bound or not.
	if _, err := h.Call(ctx, "alice", "classifier_list", nil); err == nil || !strings.Contains(err.Error(), "can't be used by a classifier") {
		t.Errorf("Call(classifier_list) = %v", err)
	}
}

// The modes follow the loop's configuration: the agents area for a folder in
// an agent's workspace.
func TestClassifierHost_ModesFollowConfig(t *testing.T) {
	cfg := classifierHostConfig(t, "http://127.0.0.1:0")
	al, host, _ := newClassifierHostLoop(t, cfg)
	alice, _ := al.GetRegistry().GetConfigured("alice")
	want := cfg.AgentModes(alice.Workspace + "/classifiers")
	if dir, file := host.Modes(alice.Workspace + "/classifiers"); dir != want.Dir || file != want.File {
		t.Errorf("Modes = %o, %o, want %o, %o", dir, file, want.Dir, want.File)
	}
}

// A model that is not configured, or whose provider is gone, is refused as
// no usable model without a request; an unknown agent too.
func TestClassifierHost_CompleteUnusableModel(t *testing.T) {
	srv := &chatServer{reply: "urgent"}
	ts := httptest.NewServer(http.HandlerFunc(srv.handle))
	defer ts.Close()
	ctx := context.Background()

	cfg := classifierHostConfig(t, ts.URL)
	_, host, _ := newClassifierHostLoop(t, cfg)
	if _, _, err := host.Complete(ctx, "carol", "s", "u"); !errors.Is(err, classifier.ErrNoModel) || !strings.Contains(err.Error(), "agent carol is not configured") {
		t.Errorf("Complete for an unknown agent = %v", err)
	}

	cfg.Agents.List[0].Models = []string{"alpha", "gamma"}
	cfg.Agents.List[0].Classifiers.Model = "gamma"
	if _, _, err := host.Complete(ctx, "alice", "s", "u"); !errors.Is(err, classifier.ErrNoModel) || !strings.Contains(err.Error(), "model gamma is not configured or not enabled") {
		t.Errorf("Complete on an unconfigured model = %v", err)
	}

	cfg.Agents.List[0].Classifiers.Model = "alpha"
	cfg.Models[0].Provider = "gone"
	if _, _, err := host.Complete(ctx, "alice", "s", "u"); !errors.Is(err, classifier.ErrNoModel) || !strings.Contains(err.Error(), "model alpha has no usable provider") {
		t.Errorf("Complete without a provider = %v", err)
	}
	if n := srv.count(); n != 0 {
		t.Errorf("the model was called %d times", n)
	}
}

// A call cancelled by a shutdown fails without putting the model in
// cooldown: the next call is made.
func TestClassifierHost_CancelledCallNoCooldown(t *testing.T) {
	srv := &chatServer{reply: "urgent"}
	ts := httptest.NewServer(http.HandlerFunc(srv.handle))
	defer ts.Close()
	_, host, _ := newClassifierHostLoop(t, classifierHostConfig(t, ts.URL))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, model, err := host.Complete(ctx, "alice", "s", "u"); err == nil || model != "alpha" {
		t.Fatalf("cancelled Complete = %q, %v", model, err)
	}
	reply, _, err := host.Complete(context.Background(), "alice", "s", "u")
	if err != nil || reply != "urgent" {
		t.Fatalf("Complete after a cancelled call = %q, %v; a shutdown put the model in cooldown", reply, err)
	}
}
