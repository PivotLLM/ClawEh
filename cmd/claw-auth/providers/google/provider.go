/******************************************************************************
 * Copyright (c) 2025-2026 Tenebris Technologies Inc.                         *
 * Please see LICENSE file for details.                                       *
 ******************************************************************************/

package google

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/cmd/claw-auth/debug"
	"github.com/PivotLLM/ClawEh/cmd/claw-auth/providers"
)

// Provider implements OAuth for Google services
type Provider struct {
	httpClient *http.Client
}

// NewProvider creates a new Google OAuth provider
func NewProvider() *Provider {
	return &Provider{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (p *Provider) GetServiceName() string {
	return "google"
}

func (p *Provider) GetDisplayName() string {
	return "Google APIs"
}

func (p *Provider) GetRequiredScopes() []string {
	return []string{
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/userinfo.profile",
		"https://www.googleapis.com/auth/calendar",
		"https://www.googleapis.com/auth/gmail.readonly",
		"https://www.googleapis.com/auth/gmail.compose",
		"https://www.googleapis.com/auth/drive",
		"https://www.googleapis.com/auth/contacts.readonly",
	}
}

func (p *Provider) GetAuthorizationEndpoint() string {
	return "https://accounts.google.com/o/oauth2/v2/auth"
}

func (p *Provider) GetTokenEndpoint() string {
	return "https://oauth2.googleapis.com/token"
}

func (p *Provider) GetDeviceCodeEndpoint() string {
	return "https://oauth2.googleapis.com/device/code"
}

func (p *Provider) SupportsDeviceFlow() bool {
	return false
}

func (p *Provider) SupportsAuthorizationCode() bool {
	return true
}

func (p *Provider) ValidateConfiguration(config *providers.ServiceConfig) error {
	if config.ClientID == "" {
		return fmt.Errorf("client_id is required for Google OAuth")
	}

	// Client secret is not required for device flow but recommended for auth code flow
	if config.ClientSecret == "" {
		// This is acceptable for device flow, but we should warn
	}

	// Validate scopes format
	if config.Scopes != "" {
		scopes := strings.Fields(config.Scopes)
		for _, scope := range scopes {
			if scope == "" {
				return fmt.Errorf("empty scope found in configuration")
			}
			if !strings.HasPrefix(scope, "https://www.googleapis.com/auth/") {
				return fmt.Errorf("invalid Google scope format: %s", scope)
			}
		}
	}

	return nil
}

func (p *Provider) CustomizeDeviceRequest(params map[string]string, config *providers.ServiceConfig) error {
	// Google-specific device flow parameters
	params["client_id"] = config.ClientID

	// Set default scopes if none provided
	scopes := config.Scopes
	if scopes == "" {
		scopes = strings.Join(p.GetRequiredScopes(), " ")
	}
	params["scope"] = scopes

	// Google-specific parameters
	params["response_type"] = "device_code"

	return nil
}

func (p *Provider) CustomizeTokenRequest(params map[string]string, config *providers.ServiceConfig) error {
	params["client_id"] = config.ClientID

	// Add client secret if available (recommended for security)
	if config.ClientSecret != "" {
		params["client_secret"] = config.ClientSecret
	}

	return nil
}

func (p *Provider) ProcessTokenResponse(response map[string]interface{}) (*providers.TokenInfo, error) {
	tokenInfo := &providers.TokenInfo{}

	// Extract access token (required)
	if accessToken, ok := response["access_token"].(string); ok {
		tokenInfo.AccessToken = accessToken
	} else {
		return nil, fmt.Errorf("access_token not found in response")
	}

	// Extract token type (usually "Bearer")
	if tokenType, ok := response["token_type"].(string); ok {
		tokenInfo.TokenType = tokenType
	} else {
		tokenInfo.TokenType = "Bearer" // Default for Google
	}

	// Extract refresh token (optional)
	if refreshToken, ok := response["refresh_token"].(string); ok {
		tokenInfo.RefreshToken = refreshToken
	}

	// Extract expiration
	if expiresIn, ok := response["expires_in"].(float64); ok {
		tokenInfo.ExpiresIn = int(expiresIn)
		expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)
		tokenInfo.ExpiresAt = &expiresAt
	}

	// Extract scope
	if scope, ok := response["scope"].(string); ok {
		tokenInfo.Scope = strings.Split(scope, " ")
	}

	// Extract ID token (if available)
	if idToken, ok := response["id_token"].(string); ok {
		tokenInfo.IDToken = idToken
	}

	return tokenInfo, nil
}

func (p *Provider) GetUserInfo(ctx context.Context, token *providers.TokenInfo) (*providers.UserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create user info request: %w", err)
	}

	req.Header.Set("Authorization", token.GetAuthorizationHeader())

	// Log the request if debug is enabled
	debug.LogHTTPRequest(req)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	// Log the response if debug is enabled
	debug.LogHTTPResponse(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("user info request failed with status: %d", resp.StatusCode)
	}

	var userInfoResp struct {
		ID            string `json:"id"`
		Email         string `json:"email"`
		Name          string `json:"name"`
		GivenName     string `json:"given_name"`
		FamilyName    string `json:"family_name"`
		Picture       string `json:"picture"`
		VerifiedEmail bool   `json:"verified_email"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&userInfoResp); err != nil {
		return nil, fmt.Errorf("failed to decode user info response: %w", err)
	}

	return &providers.UserInfo{
		ID:          userInfoResp.ID,
		Email:       userInfoResp.Email,
		Name:        userInfoResp.Name,
		DisplayName: userInfoResp.Name,
	}, nil
}

// GetExtendedScopes returns common extended scopes for Google services
func (p *Provider) GetExtendedScopes() map[string][]string {
	return map[string][]string{
		"calendar": {
			"https://www.googleapis.com/auth/calendar",
			"https://www.googleapis.com/auth/calendar.readonly",
		},
		"gmail": {
			"https://www.googleapis.com/auth/gmail.readonly",
			"https://www.googleapis.com/auth/gmail.compose",
			"https://www.googleapis.com/auth/gmail.modify",
		},
		"contacts": {
			"https://www.googleapis.com/auth/contacts.readonly",
		},
		"drive": {
			"https://www.googleapis.com/auth/drive",
			"https://www.googleapis.com/auth/drive.readonly",
			"https://www.googleapis.com/auth/drive.file",
		},
		"photos": {
			"https://www.googleapis.com/auth/photoslibrary.readonly",
		},
		"youtube": {
			"https://www.googleapis.com/auth/youtube.readonly",
			"https://www.googleapis.com/auth/youtube.upload",
		},
	}
}

// ValidateToken validates a Google OAuth token by making a test API call
func (p *Provider) ValidateToken(ctx context.Context, token *providers.TokenInfo) error {
	// Use the tokeninfo endpoint to validate the token
	tokenInfoURL := "https://oauth2.googleapis.com/tokeninfo"
	values := url.Values{}
	values.Set("access_token", token.AccessToken)

	req, err := http.NewRequestWithContext(ctx, "POST", tokenInfoURL, strings.NewReader(values.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create token validation request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Log the request if debug is enabled
	debug.LogHTTPRequest(req)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to validate token: %w", err)
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	// Log the response if debug is enabled
	debug.LogHTTPResponse(resp)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token validation failed with status: %d", resp.StatusCode)
	}

	var tokenInfoResp struct {
		Audience  string `json:"aud"`
		ClientID  string `json:"azp"`
		ExpiresIn string `json:"expires_in"`
		Scope     string `json:"scope"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&tokenInfoResp); err != nil {
		return fmt.Errorf("failed to decode token info response: %w", err)
	}

	// Additional validation could be performed here
	return nil
}
