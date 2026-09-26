// ClawEh
// License: MIT

package api

import (
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/logger"
)

// registerAuditRoutes binds the audit log read endpoint.
func (h *Handler) registerAuditRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/audit", h.handleListAudit)
}

// auditListResponse is the GET /api/audit body. NextBeforeID is the id to pass
// as before_id to fetch the page after this one; 0 when this page was empty.
type auditListResponse struct {
	Events       []audit.Event `json:"events"`
	NextBeforeID int64         `json:"next_before_id"`
	// Dropped is how many events the store discarded since start because its
	// write queue was full; non-zero means the trail has gaps.
	Dropped uint64 `json:"dropped"`
}

// handleListAudit returns audit rows, newest first.
//
//	GET /api/audit?since=&until=&kind=&agent=&session=&limit=&before_id=
//
// since/until are RFC 3339 timestamps; limit is clamped to 1000; before_id
// pages backwards from a previous response's next_before_id.
func (h *Handler) handleListAudit(w http.ResponseWriter, r *http.Request) {
	store := audit.Default()
	if store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "audit log is not enabled")
		return
	}
	q := r.URL.Query()
	f := audit.Filter{
		Kind:    strings.TrimSpace(q.Get("kind")),
		Agent:   strings.TrimSpace(q.Get("agent")),
		Session: strings.TrimSpace(q.Get("session")),
	}
	var err error
	if f.Since, err = parseAuditTime(q.Get("since")); err != nil {
		writeJSONError(w, http.StatusBadRequest, "since: "+err.Error())
		return
	}
	if f.Until, err = parseAuditTime(q.Get("until")); err != nil {
		writeJSONError(w, http.StatusBadRequest, "until: "+err.Error())
		return
	}
	if s := q.Get("limit"); s != "" {
		if f.Limit, err = strconv.Atoi(s); err != nil || f.Limit < 0 {
			writeJSONError(w, http.StatusBadRequest, "limit must be a non-negative integer")
			return
		}
	}
	if s := q.Get("before_id"); s != "" {
		if f.BeforeID, err = strconv.ParseInt(s, 10, 64); err != nil || f.BeforeID < 0 {
			writeJSONError(w, http.StatusBadRequest, "before_id must be a non-negative integer")
			return
		}
	}

	events, err := store.Query(r.Context(), f)
	if err != nil {
		logger.WarnCF("api", "audit query failed", map[string]any{"error": err.Error()})
		writeJSONError(w, http.StatusInternalServerError, "audit query failed")
		return
	}
	resp := auditListResponse{Events: events, Dropped: store.Dropped()}
	if len(events) > 0 {
		resp.NextBeforeID = events[len(events)-1].ID
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	encodeJSON(w, resp)
}

func parseAuditTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, s)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	encodeJSON(w, map[string]string{"error": msg})
}

// recordConfigWrite writes a config_write audit row for a save that just
// succeeded: who (the authenticated username, when the auth middleware set
// one), from where (client IP), and which top-level config keys changed. Values
// are never recorded — a changed key may be a credential. oldCfg may be nil
// when the previous config could not be loaded; every key then counts as
// changed.
func recordConfigWrite(r *http.Request, oldCfg, newCfg *config.Config) {
	store := audit.Default()
	if store == nil {
		return
	}
	keys := changedTopLevelKeys(oldCfg, newCfg)
	details, err := json.Marshal(map[string]any{"keys": keys})
	if err != nil {
		details = []byte(`{"keys":[]}`)
	}
	store.Record(audit.Event{
		Kind:    audit.KindConfigWrite,
		Actor:   audit.ActorFromContext(r.Context()),
		Sender:  clientIP(r),
		Summary: strings.Join(keys, ", "),
		Details: string(details),
		Outcome: audit.OutcomeOK,
	})
}

// changedTopLevelKeys diffs two configs at the top level of their JSON form and
// returns the sorted keys whose serialised value differs (added, removed or
// changed). Only key names come back, never what is under them.
func changedTopLevelKeys(oldCfg, newCfg *config.Config) []string {
	oldMap := topLevelJSON(oldCfg)
	newMap := topLevelJSON(newCfg)
	seen := map[string]struct{}{}
	for k, nv := range newMap {
		if ov, ok := oldMap[k]; !ok || string(ov) != string(nv) {
			seen[k] = struct{}{}
		}
	}
	for k := range oldMap {
		if _, ok := newMap[k]; !ok {
			seen[k] = struct{}{}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func topLevelJSON(cfg *config.Config) map[string]json.RawMessage {
	if cfg == nil {
		return nil
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}

// clientIP is the TCP peer of the request, without the port.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
