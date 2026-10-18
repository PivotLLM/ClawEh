// ClawEh
// License: MIT

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/ccv3"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/audit"
)

const watsonV3 = `{"spec":"chara_card_v3","spec_version":"3.0","data":{"name":"Watson","description":"A doctor."}}`

// cardsHandler is a handler on a fresh config whose library holds "watson",
// attached to the agent Main.
func cardsHandler(t *testing.T) (*http.ServeMux, string) {
	t.Helper()
	configPath := setupTestEnv(t)
	initTestAudit(t)
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents.List[0].CharacterCard = "Watson"
	if serr := config.SaveConfig(configPath, cfg); serr != nil {
		t.Fatal(serr)
	}
	store, err := ccv3.Open(cfg.CardsPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("watson", json.RawMessage(`{"name":"Watson","description":"A doctor.","tags":["fiction"]}`)); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux, cfg.CardsPath()
}

func importCard(t *testing.T, mux *http.ServeMux, id, filename string, content []byte, replace bool) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("id", id); err != nil {
		t.Fatal(err)
	}
	if replace {
		if err := w.WriteField("replace", "true"); err != nil {
			t.Fatal(err)
		}
	}
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/cards/import", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.RemoteAddr = "192.0.2.7:51000"
	req = req.WithContext(audit.WithActor(req.Context(), "admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func cardRows(t *testing.T, kind string) []audit.Event {
	t.Helper()
	flushAudit(t)
	rows, err := audit.Default().Query(context.Background(), audit.Filter{Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestCards_ListAndGet(t *testing.T) {
	mux, _ := cardsHandler(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/cards", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/cards = %d: %s", rec.Code, rec.Body.String())
	}
	var resp cardsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Cards) != 1 || resp.Cards[0].ID != "watson" || resp.Cards[0].Name != "Watson" ||
		strings.Join(resp.Cards[0].AttachedTo, ",") != "main" || strings.Join(resp.Cards[0].Tags, ",") != "fiction" {
		t.Errorf("cards = %+v", resp.Cards)
	}
	if resp.Invalid == nil || len(resp.Invalid) != 0 {
		t.Errorf("invalid = %#v, want an empty list", resp.Invalid)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/cards/Watson", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Disposition"), `filename="watson.json"`) {
		t.Fatalf("GET card = %d %q: %s", rec.Code, rec.Header().Get("Content-Disposition"), rec.Body.String())
	}
	var card ccv3.Card
	if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil || card.Spec != ccv3.SpecV3 {
		t.Errorf("card body = %s (%v)", rec.Body.String(), err)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/cards/nobody", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET missing card = %d", rec.Code)
	}
}

func TestCards_ImportAndReplace(t *testing.T) {
	mux, dir := cardsHandler(t)

	rec := importCard(t, mux, "Holmes", "holmes.json", []byte(watsonV3), false)
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "holmes.json")); err != nil {
		t.Errorf("imported card not written: %v", err)
	}
	var resp struct {
		ID     string `json:"id"`
		Format string `json:"format"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.ID != "holmes" || resp.Format != string(ccv3.FormatV3JSON) {
		t.Errorf("import response = %s", rec.Body.String())
	}
	rows := cardRows(t, audit.KindCardImport)
	if len(rows) != 1 || rows[0].Actor != "admin" || rows[0].Sender != "192.0.2.7" || rows[0].Summary != "holmes" ||
		!strings.Contains(rows[0].Details, `"format":"v3-json"`) || strings.Contains(rows[0].Details, "A doctor") {
		t.Errorf("card_import rows = %+v", rows)
	}

	if rec = importCard(t, mux, "holmes", "holmes.json", []byte(watsonV3), false); rec.Code != http.StatusConflict {
		t.Errorf("import over an existing card = %d: %s", rec.Code, rec.Body.String())
	}
	if rec = importCard(t, mux, "holmes", "holmes.json", []byte(watsonV3), true); rec.Code != http.StatusOK {
		t.Errorf("replace = %d: %s", rec.Code, rec.Body.String())
	}
	if rec = importCard(t, mux, "old", "old.json", []byte(`{"name":"Old","description":"V1"}`), false); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "This is a V1 card; convert it to V2 or V3 first.") {
		t.Errorf("V1 import = %d: %s", rec.Code, rec.Body.String())
	}
	if rec = importCard(t, mux, "", "x.json", []byte(watsonV3), false); rec.Code != http.StatusBadRequest {
		t.Errorf("import without id = %d", rec.Code)
	}
	if rec = importCard(t, mux, "big", "big.png", make([]byte, ccv3.MaxImportBytes+1), false); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "larger than 8 MiB") {
		t.Errorf("oversized import = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCards_DeleteRefusedWhileAttached(t *testing.T) {
	mux, dir := cardsHandler(t)
	del := func(id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/api/cards/"+id, nil)
		req = req.WithContext(audit.WithActor(req.Context(), "admin"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := del("WATSON"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `Card \"WATSON\" is attached to agent Main; detach it first.`) {
		t.Errorf("delete attached = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := importCard(t, mux, "holmes", "holmes.json", []byte(watsonV3), false); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if rec := del("holmes"); rec.Code != http.StatusOK {
		t.Errorf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "holmes.json")); !os.IsNotExist(err) {
		t.Error("deleted card still on disk")
	}
	if rows := cardRows(t, audit.KindCardDelete); len(rows) != 1 || rows[0].Summary != "holmes" || rows[0].Actor != "admin" {
		t.Errorf("card_delete rows = %+v", rows)
	}
	if rec := del("holmes"); rec.Code != http.StatusNotFound {
		t.Errorf("delete missing = %d", rec.Code)
	}
}

// A config save that attaches a card the library does not have is refused
// naming the agent; one that keeps an already-missing reference, or attaches
// an existing card, goes through.
func TestConfigSave_RefusesNewMissingCard(t *testing.T) {
	mux, _ := cardsHandler(t)
	patch := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	rec := patch(`{"agents":{"list":[{"id":"main","name":"Main","default":true,"character_card":"nobody"}]}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `Main: character card \"nobody\" does not exist.`) {
		t.Errorf("attach missing = %d: %s", rec.Code, rec.Body.String())
	}
	if rec = patch(`{"agents":{"list":[{"id":"main","name":"Main","default":true,"character_card":"watson"}]}}`); rec.Code != http.StatusOK {
		t.Errorf("attach existing = %d: %s", rec.Code, rec.Body.String())
	}
	// Remove the card behind the agent's back; an unrelated save still works.
	store, err := ccv3.Open(filepath.Join(os.Getenv("CLAW_HOME"), "cards"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("watson"); err != nil {
		t.Fatal(err)
	}
	if rec = patch(`{"logging":{"level":"debug"}}`); rec.Code != http.StatusOK {
		t.Errorf("unrelated save with a dangling card = %d: %s", rec.Code, rec.Body.String())
	}
}
