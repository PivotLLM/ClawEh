package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/utils"
)

const (
	defaultClawHubTimeout  = 30 * time.Second
	defaultMaxZipSize      = 50 * 1024 * 1024 // 50 MB
	defaultMaxResponseSize = 2 * 1024 * 1024  // 2 MB
)

// ClawHubRegistry implements SkillRegistry for the ClawHub platform.
type ClawHubRegistry struct {
	baseURL         string
	authToken       string // Optional - for elevated rate limits
	searchPath      string // Search API
	skillsPath      string // For retrieving skill metadata
	downloadPath    string // For fetching ZIP files for download
	maxZipSize      int
	maxResponseSize int
	client          *http.Client
}

// NewClawHubRegistry creates a new ClawHub registry client from config.
func NewClawHubRegistry(cfg ClawHubConfig) *ClawHubRegistry {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://clawhub.ai"
	}
	searchPath := cfg.SearchPath
	if searchPath == "" {
		searchPath = "/api/v1/search"
	}
	skillsPath := cfg.SkillsPath
	if skillsPath == "" {
		skillsPath = "/api/v1/skills"
	}
	downloadPath := cfg.DownloadPath
	if downloadPath == "" {
		downloadPath = "/api/v1/download"
	}

	timeout := defaultClawHubTimeout
	if cfg.Timeout > 0 {
		timeout = time.Duration(cfg.Timeout) * time.Second
	}

	maxZip := defaultMaxZipSize
	if cfg.MaxZipSize > 0 {
		maxZip = cfg.MaxZipSize
	}

	maxResp := defaultMaxResponseSize
	if cfg.MaxResponseSize > 0 {
		maxResp = cfg.MaxResponseSize
	}

	return &ClawHubRegistry{
		baseURL:         baseURL,
		authToken:       cfg.AuthToken,
		searchPath:      searchPath,
		skillsPath:      skillsPath,
		downloadPath:    downloadPath,
		maxZipSize:      maxZip,
		maxResponseSize: maxResp,
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        5,
				IdleConnTimeout:     30 * time.Second,
				TLSHandshakeTimeout: 10 * time.Second,
			},
		},
	}
}

func (c *ClawHubRegistry) Name() string {
	return "clawhub"
}

// --- Search ---

type clawhubSearchResponse struct {
	Results []clawhubSearchResult `json:"results"`
}

type clawhubSearchResult struct {
	Score       float64 `json:"score"`
	Slug        *string `json:"slug"`
	DisplayName *string `json:"displayName"`
	Summary     *string `json:"summary"`
	Version     *string `json:"version"`
}

func (c *ClawHubRegistry) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	u, err := url.Parse(c.baseURL + c.searchPath)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}

	q := u.Query()
	q.Set("q", query)
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	u.RawQuery = q.Encode()

	body, err := c.doGet(ctx, u.String())
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}

	var resp clawhubSearchResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse search response: %w", err)
	}

	results := make([]SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		slug := utils.DerefStr(r.Slug, "")
		if slug == "" {
			continue
		}

		summary := utils.DerefStr(r.Summary, "")
		if summary == "" {
			continue
		}

		displayName := utils.DerefStr(r.DisplayName, "")
		if displayName == "" {
			displayName = slug
		}

		results = append(results, SearchResult{
			Score:        r.Score,
			Slug:         slug,
			DisplayName:  displayName,
			Summary:      summary,
			Version:      utils.DerefStr(r.Version, ""),
			RegistryName: c.Name(),
		})
	}

	return results, nil
}

// --- GetSkillMeta ---

type clawhubSkillResponse struct {
	Slug          string                 `json:"slug"`
	DisplayName   string                 `json:"displayName"`
	Summary       string                 `json:"summary"`
	LatestVersion *clawhubVersionInfo    `json:"latestVersion"`
	Moderation    *clawhubModerationInfo `json:"moderation"`
}

type clawhubVersionInfo struct {
	Version string `json:"version"`
}

type clawhubModerationInfo struct {
	IsMalwareBlocked bool `json:"isMalwareBlocked"`
	IsSuspicious     bool `json:"isSuspicious"`
}

func (c *ClawHubRegistry) GetSkillMeta(ctx context.Context, slug string) (*SkillMeta, error) {
	if err := utils.ValidateSkillIdentifier(slug); err != nil {
		return nil, fmt.Errorf("invalid slug %q: error: %s", slug, err.Error())
	}

	u := c.baseURL + c.skillsPath + "/" + url.PathEscape(slug)

	body, err := c.doGet(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("skill metadata request failed: %w", err)
	}

	var resp clawhubSkillResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse skill metadata: %w", err)
	}

	meta := &SkillMeta{
		Slug:         resp.Slug,
		DisplayName:  resp.DisplayName,
		Summary:      resp.Summary,
		RegistryName: c.Name(),
	}

	if resp.LatestVersion != nil {
		meta.LatestVersion = resp.LatestVersion.Version
	}
	if resp.Moderation != nil {
		meta.IsMalwareBlocked = resp.Moderation.IsMalwareBlocked
		meta.IsSuspicious = resp.Moderation.IsSuspicious
	}

	return meta, nil
}

// --- DownloadAndInstall ---

// DownloadAndInstall fetches metadata (with fallback), resolves version,
// downloads the skill ZIP, and extracts it to targetDir.
// Returns an InstallResult for the caller to use for moderation decisions.
func (c *ClawHubRegistry) DownloadAndInstall(
	ctx context.Context,
	slug, version, targetDir string,
) (*InstallResult, error) {
	if err := utils.ValidateSkillIdentifier(slug); err != nil {
		return nil, fmt.Errorf("invalid slug %q: error: %s", slug, err.Error())
	}

	// Step 1: Fetch metadata (with fallback).
	result := &InstallResult{}
	meta, err := c.GetSkillMeta(ctx, slug)
	if err != nil {
		// Fallback: proceed without metadata.
		meta = nil
	}

	if meta != nil {
		result.IsMalwareBlocked = meta.IsMalwareBlocked
		result.IsSuspicious = meta.IsSuspicious
		result.Summary = meta.Summary
	}

	// Step 2: Resolve version.
	installVersion := version
	if installVersion == "" && meta != nil {
		installVersion = meta.LatestVersion
	}
	if installVersion == "" {
		installVersion = "latest"
	}
	result.Version = installVersion

	// Step 3: Download ZIP to temp file (streams in ~32KB chunks).
	u, err := url.Parse(c.baseURL + c.downloadPath)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}

	q := u.Query()
	q.Set("slug", slug)
	if installVersion != "latest" {
		q.Set("version", installVersion)
	}
	u.RawQuery = q.Encode()

	tmpPath, err := c.downloadToTempFileWithRetry(ctx, u.String())
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer removeTempFile(tmpPath)

	// Step 4: Extract from file on disk.
	if err := utils.ExtractZipFile(tmpPath, targetDir); err != nil {
		return nil, err
	}

	return result, nil
}

// --- HTTP helper ---

func (c *ClawHubRegistry) doGet(ctx context.Context, urlStr string) ([]byte, error) {
	req, err := c.newGetRequest(ctx, urlStr, "application/json")
	if err != nil {
		return nil, err
	}

	resp, err := utils.DoRequestWithRetry(c.client, req)
	if err != nil {
		return nil, err
	}
	defer func() { utils.CloseQuietly(resp.Body) }()

	// Limit response body read to prevent memory issues.
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(c.maxResponseSize)))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

func (c *ClawHubRegistry) newGetRequest(ctx context.Context, urlStr, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	return req, nil
}

func (c *ClawHubRegistry) downloadToTempFileWithRetry(ctx context.Context, urlStr string) (string, error) {
	req, err := c.newGetRequest(ctx, urlStr, "application/zip")
	if err != nil {
		return "", err
	}

	resp, err := utils.DoRequestWithRetry(c.client, req)
	if err != nil {
		return "", err
	}
	defer func() { utils.CloseQuietly(resp.Body) }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Whatever was read of the body is context for the error; a short or
		// failed read just means less context.
		errBody := make([]byte, 512)
		n, readErr := io.ReadFull(resp.Body, errBody)
		if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
			logger.DebugCF("skills", "failed to read error response body", map[string]any{"error": readErr.Error()})
		}
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(errBody[:n]))
	}

	tmpFile, err := os.CreateTemp("", "claw-dl-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	cleanup := func() {
		// The copy error is what the caller sees; the close error is noise on
		// top of it, but a leftover temp file is worth a warning.
		utils.CloseQuietly(tmpFile)
		removeTempFile(tmpPath)
	}

	src := io.LimitReader(resp.Body, int64(c.maxZipSize)+1)
	written, err := io.Copy(tmpFile, src)
	if err != nil {
		cleanup()
		return "", fmt.Errorf("download write failed: %w", err)
	}

	if written > int64(c.maxZipSize) {
		cleanup()
		return "", fmt.Errorf("download too large: %d bytes (max %d)", written, c.maxZipSize)
	}

	if err := tmpFile.Close(); err != nil {
		removeTempFile(tmpPath)
		return "", fmt.Errorf("failed to close temp file: %w", err)
	}

	return tmpPath, nil
}

// removeTempFile removes a temp download and warns if it is left behind.
func removeTempFile(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		logger.WarnCF("skills", "failed to remove temp file", map[string]any{"path": path, "error": err.Error()})
	}
}
