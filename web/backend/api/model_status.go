package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

const modelProbeTimeout = 800 * time.Millisecond

var (
	probeTCPServiceFunc            = probeTCPService
	probeOllamaModelFunc           = probeOllamaModel
	probeOpenAICompatibleModelFunc = probeOpenAICompatibleModel
)

func hasModelConfiguration(prov *config.Provider, m config.ModelConfig) bool {
	if config.IsCLIProtocol(prov.Protocol) {
		return true
	}
	if hasLocalAPIBase(prov.BaseURL) {
		return true
	}
	return strings.TrimSpace(prov.APIKey) != ""
}

// isModelConfigured reports whether a model is currently available to use.
// Local models must be reachable; remote/API-key models only need saved config.
func isModelConfigured(prov *config.Provider, m config.ModelConfig) bool {
	if !hasModelConfiguration(prov, m) {
		return false
	}
	if requiresRuntimeProbe(prov) {
		return probeLocalModelAvailability(prov, m)
	}
	return true
}

func requiresRuntimeProbe(prov *config.Provider) bool {
	if config.IsCLIProtocol(prov.Protocol) {
		return true
	}
	return hasLocalAPIBase(prov.BaseURL)
}

func probeLocalModelAvailability(prov *config.Provider, m config.ModelConfig) bool {
	// CLI providers authenticate out-of-band and are treated as available.
	if config.IsCLIProtocol(prov.Protocol) {
		return true
	}
	apiBase := normalizeModelProbeAPIBase(prov.BaseURL)
	if hasLocalAPIBase(apiBase) {
		return probeOpenAICompatibleModelFunc(apiBase, m.Model)
	}
	return false
}

func normalizeModelProbeAPIBase(raw string) string {
	u, err := parseAPIBase(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}

	switch strings.ToLower(u.Hostname()) {
	case "0.0.0.0":
		u.Host = net.JoinHostPort("127.0.0.1", u.Port())
	case "::":
		u.Host = net.JoinHostPort("::1", u.Port())
	default:
		return strings.TrimSpace(raw)
	}

	if u.Port() == "" {
		u.Host = u.Hostname()
	}

	return u.String()
}

func hasLocalAPIBase(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}

	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		u, err = url.Parse("//" + raw)
		if err != nil {
			return false
		}
	}

	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	default:
		return false
	}
}

func probeTCPService(raw string) bool {
	hostPort, err := hostPortFromAPIBase(raw)
	if err != nil {
		return false
	}

	conn, err := net.DialTimeout("tcp", hostPort, modelProbeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func probeOllamaModel(apiBase, modelID string) bool {
	root, err := apiRootFromAPIBase(apiBase)
	if err != nil {
		return false
	}

	var resp struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := getJSON(root+"/api/tags", &resp); err != nil {
		return false
	}

	for _, model := range resp.Models {
		if ollamaModelMatches(model.Name, modelID) || ollamaModelMatches(model.Model, modelID) {
			return true
		}
	}
	return false
}

func probeOpenAICompatibleModel(apiBase, modelID string) bool {
	if strings.TrimSpace(apiBase) == "" {
		return false
	}

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := getJSON(strings.TrimRight(strings.TrimSpace(apiBase), "/")+"/models", &resp); err != nil {
		return false
	}

	for _, model := range resp.Data {
		if strings.EqualFold(strings.TrimSpace(model.ID), modelID) {
			return true
		}
	}
	return false
}

func getJSON(rawURL string, out any) error {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: modelProbeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

func apiRootFromAPIBase(raw string) (string, error) {
	u, err := parseAPIBase(raw)
	if err != nil {
		return "", err
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String(), nil
}

func hostPortFromAPIBase(raw string) (string, error) {
	u, err := parseAPIBase(raw)
	if err != nil {
		return "", err
	}

	if port := u.Port(); port != "" {
		return u.Host, nil
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return net.JoinHostPort(u.Hostname(), "443"), nil
	default:
		return net.JoinHostPort(u.Hostname(), "80"), nil
	}
}

func parseAPIBase(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty api base")
	}

	u, err := url.Parse(raw)
	if err == nil && u.Hostname() != "" {
		return u, nil
	}

	u, err = url.Parse("//" + raw)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("invalid api base %q", raw)
	}
	if u.Scheme == "" {
		u.Scheme = "http"
	}
	return u, nil
}

func ollamaModelMatches(candidate, want string) bool {
	candidate = strings.TrimSpace(candidate)
	want = strings.TrimSpace(want)
	if candidate == "" || want == "" {
		return false
	}
	if strings.EqualFold(candidate, want) {
		return true
	}

	base, _, _ := strings.Cut(candidate, ":")
	return strings.EqualFold(base, want)
}
