/******************************************************************************
 * Copyright (c) 2025-2026 Tenebris Technologies Inc.                         *
 * Please see LICENSE file for details.                                       *
 ******************************************************************************/

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/PivotLLM/ClawEh/cmd/claw-auth/debug"
	"github.com/PivotLLM/ClawEh/cmd/claw-auth/providers"
)

// Client handles communication with MCPFusion server
type Client struct {
	baseURL    string
	apiToken   string
	httpClient *http.Client
}

// NewClient creates a new MCP client
func NewClient(baseURL, apiToken string) *Client {
	return &Client{
		baseURL:  baseURL,
		apiToken: apiToken,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// TokenRequest represents a request to store OAuth tokens
type TokenRequest struct {
	Service      string            `json:"service"`
	AccessToken  string            `json:"access_token"`
	RefreshToken string            `json:"refresh_token"`
	ExpiresIn    int               `json:"expires_in,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// TokenResponse represents the response from storing OAuth tokens
type TokenResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	TokenID string `json:"token_id,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// PingResponse represents the response from the ping endpoint
type PingResponse struct {
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	TenantID  string `json:"tenant_id,omitempty"`
	Timestamp int64  `json:"timestamp,omitempty"`
}

// Ping tests connectivity and authentication with the MCPFusion server
func (c *Client) Ping(ctx context.Context) (*PingResponse, error) {
	endpoint := fmt.Sprintf("%s/ping", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create ping request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("User-Agent", "fusion-auth/1.0")

	// Log the request if debug is enabled
	debug.LogHTTPRequest(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ping request failed: %w", err)
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	// Log the response if debug is enabled
	debug.LogHTTPResponse(resp)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read ping response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("authentication failed: invalid API token")
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ping failed with status %d: %s", resp.StatusCode, string(body))
	}

	var pingResp PingResponse
	if err := json.Unmarshal(body, &pingResp); err != nil {
		return nil, fmt.Errorf("failed to decode ping response: %w", err)
	}

	return &pingResp, nil
}

// StoreTokens sends OAuth tokens to MCPFusion for storage
func (c *Client) StoreTokens(ctx context.Context, service, accessToken, refreshToken string, expiresIn int, metadata map[string]string) (*TokenResponse, error) {
	req := &TokenRequest{
		Service:      service,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    expiresIn,
		Metadata:     metadata,
	}

	endpoint := fmt.Sprintf("%s/api/v1/oauth/tokens", c.baseURL)

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal token request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewBuffer(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiToken)
	httpReq.Header.Set("User-Agent", "fusion-oauth/1.0")

	// Log the request if debug is enabled
	debug.LogHTTPRequest(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send token request: %w", err)
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	// Log the response if debug is enabled
	debug.LogHTTPResponse(resp)

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		errorMsg := "unknown error"
		if tokenResp.Error != nil && tokenResp.Error.Message != "" {
			errorMsg = tokenResp.Error.Message
		} else if tokenResp.Message != "" {
			errorMsg = tokenResp.Message
		}
		return &tokenResp, fmt.Errorf("token storage failed with status %d: %s", resp.StatusCode, errorMsg)
	}

	return &tokenResp, nil
}

// HealthCheck checks if the MCPFusion server is accessible
func (c *Client) HealthCheck(ctx context.Context) error {
	endpoint := fmt.Sprintf("%s/health", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create health check request: %w", err)
	}

	req.Header.Set("User-Agent", "fusion-oauth/1.0")

	// Log the request if debug is enabled
	debug.LogHTTPRequest(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	// Log the response if debug is enabled
	debug.LogHTTPResponse(resp)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("MCPFusion server health check failed with status: %d", resp.StatusCode)
	}

	return nil
}

// AuthCheck verifies that the API token is valid
func (c *Client) AuthCheck(ctx context.Context) error {
	endpoint := fmt.Sprintf("%s/api/v1/auth/verify", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create auth check request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("User-Agent", "fusion-oauth/1.0")

	// Log the request if debug is enabled
	debug.LogHTTPRequest(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("auth check failed: %w", err)
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	// Log the response if debug is enabled
	debug.LogHTTPResponse(resp)

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("API token is invalid or expired")
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth check failed with status: %d", resp.StatusCode)
	}

	return nil
}

// GetServiceConfig retrieves OAuth configuration for a specific service
func (c *Client) GetServiceConfig(ctx context.Context, serviceName string) (*ServiceConfigResponse, error) {
	endpoint := fmt.Sprintf("%s/api/v1/services/%s/config", c.baseURL, serviceName)

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create service config request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("User-Agent", "fusion-oauth/1.0")

	// Log the request if debug is enabled
	debug.LogHTTPRequest(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("service config request failed: %w", err)
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	// Log the response if debug is enabled
	debug.LogHTTPResponse(resp)

	var configResp ServiceConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&configResp); err != nil {
		return nil, fmt.Errorf("failed to decode service config response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return &configResp, fmt.Errorf("service config request failed with status %d: %s", resp.StatusCode, configResp.Message)
	}

	return &configResp, nil
}

// ServiceConfigResponse represents the response from getting service config
type ServiceConfigResponse struct {
	Success     bool               `json:"success"`
	Message     string             `json:"message"`
	ServiceName string             `json:"service_name,omitempty"`
	Config      *ServiceConfigData `json:"config,omitempty"`
}

// ServiceConfigData represents the service configuration data returned by the server
type ServiceConfigData struct {
	ServiceName    string                 `json:"service_name,omitempty"`
	OAuthAvailable bool                   `json:"oauth_available,omitempty"`
	AuthTypeStr    string                 `json:"auth_type,omitempty"`
	ClientID       string                 `json:"client_id,omitempty"`
	ClientSecret   string                 `json:"client_secret,omitempty"`
	Scopes         string                 `json:"scopes,omitempty"`
	Instructions   string                 `json:"instructions,omitempty"`
	Fields         []CredentialField      `json:"fields,omitempty"`
	Endpoints      map[string]string      `json:"endpoints,omitempty"`
	Extra          map[string]interface{} `json:"-"`
}

// CredentialField represents a field definition for user_credentials auth
type CredentialField struct {
	Name        string `json:"name"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	Location    string `json:"location"`
	ParamName   string `json:"paramName,omitempty"`
}

// AuthType returns the auth type and whether it was present
func (s *ServiceConfigData) AuthType() (string, bool) {
	if s == nil || s.AuthTypeStr == "" {
		return "", false
	}
	return s.AuthTypeStr, true
}

// NotifySuccess sends a success notification to MCPFusion
func (c *Client) NotifySuccess(ctx context.Context, serviceName string, userInfo *providers.UserInfo) error {
	endpoint := fmt.Sprintf("%s/api/v1/oauth/success", c.baseURL)

	notification := struct {
		Service   string              `json:"service"`
		UserInfo  *providers.UserInfo `json:"user_info"`
		Timestamp time.Time           `json:"timestamp"`
	}{
		Service:   serviceName,
		UserInfo:  userInfo,
		Timestamp: time.Now(),
	}

	payload, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("failed to marshal success notification: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("failed to create success notification request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("User-Agent", "fusion-oauth/1.0")

	// Log the request if debug is enabled
	debug.LogHTTPRequest(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("success notification failed: %w", err)
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	// Log the response if debug is enabled
	debug.LogHTTPResponse(resp)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("success notification failed with status: %d", resp.StatusCode)
	}

	return nil
}

// NotifyError sends an error notification to MCPFusion
func (c *Client) NotifyError(ctx context.Context, serviceName string, errorMsg string) error {
	endpoint := fmt.Sprintf("%s/api/v1/oauth/error", c.baseURL)

	notification := struct {
		Service   string    `json:"service"`
		Error     string    `json:"error"`
		Timestamp time.Time `json:"timestamp"`
	}{
		Service:   serviceName,
		Error:     errorMsg,
		Timestamp: time.Now(),
	}

	payload, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("failed to marshal error notification: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("failed to create error notification request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("User-Agent", "fusion-oauth/1.0")

	// Log the request if debug is enabled
	debug.LogHTTPRequest(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("error notification failed: %w", err)
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	// Log the response if debug is enabled
	debug.LogHTTPResponse(resp)

	// Don't fail if error notification fails - it's not critical
	return nil
}
