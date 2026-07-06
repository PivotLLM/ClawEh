/******************************************************************************
 * Copyright (c) 2025-2026 Tenebris Technologies Inc.                         *
 * Please see LICENSE file for details.                                       *
 ******************************************************************************/

package providers

import (
	"context"
	"fmt"
	"time"
)

// OAuthProvider defines the interface that all OAuth service providers must implement
type OAuthProvider interface {
	// GetServiceName returns the unique name identifier for this service
	GetServiceName() string

	// GetDisplayName returns the human-readable display name for this service
	GetDisplayName() string

	// GetRequiredScopes returns the default/required scopes for this service
	GetRequiredScopes() []string

	// GetAuthorizationEndpoint returns the OAuth authorization endpoint URL
	GetAuthorizationEndpoint() string

	// GetTokenEndpoint returns the OAuth token endpoint URL
	GetTokenEndpoint() string

	// GetDeviceCodeEndpoint returns the device code endpoint URL (if supported)
	GetDeviceCodeEndpoint() string

	// SupportsDeviceFlow returns true if the provider supports OAuth device flow
	SupportsDeviceFlow() bool

	// SupportsAuthorizationCode returns true if the provider supports authorization code flow
	SupportsAuthorizationCode() bool

	// ValidateConfiguration validates the provider-specific configuration
	ValidateConfiguration(config *ServiceConfig) error

	// CustomizeDeviceRequest allows providers to customize the device code request
	CustomizeDeviceRequest(params map[string]string, config *ServiceConfig) error

	// CustomizeTokenRequest allows providers to customize the token exchange request
	CustomizeTokenRequest(params map[string]string, config *ServiceConfig) error

	// ProcessTokenResponse allows providers to process and validate token responses
	ProcessTokenResponse(response map[string]interface{}) (*TokenInfo, error)

	// GetUserInfo retrieves user information using the access token (for verification)
	GetUserInfo(ctx context.Context, token *TokenInfo) (*UserInfo, error)
}

// ServiceConfig holds configuration for a specific OAuth service
type ServiceConfig struct {
	ServiceName      string            `json:"service_name"`
	ClientID         string            `json:"client_id"`
	ClientSecret     string            `json:"client_secret,omitempty"` // For services that require it
	TenantID         string            `json:"tenant_id,omitempty"`     // For Microsoft 365
	Scopes           string            `json:"scopes,omitempty"`
	RedirectURI      string            `json:"redirect_uri,omitempty"`
	TokenURL         string            `json:"token_url,omitempty"`         // Override provider's default token endpoint
	AuthorizationURL string            `json:"authorization_url,omitempty"` // Override provider's default auth endpoint
	Metadata         map[string]string `json:"metadata,omitempty"`
}

// TokenInfo represents OAuth token information
type TokenInfo struct {
	AccessToken  string            `json:"access_token"`
	RefreshToken string            `json:"refresh_token,omitempty"`
	TokenType    string            `json:"token_type"`
	ExpiresIn    int               `json:"expires_in,omitempty"`
	ExpiresAt    *time.Time        `json:"expires_at,omitempty"`
	Scope        []string          `json:"scope,omitempty"`
	IDToken      string            `json:"id_token,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// UserInfo represents basic user information for verification
type UserInfo struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
}

// DeviceCodeResponse represents the response from device code endpoint
type DeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	Message                 string `json:"message,omitempty"`
}

// ProviderRegistry manages all available OAuth providers
type ProviderRegistry struct {
	providers map[string]OAuthProvider
}

// NewProviderRegistry creates a new provider registry
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		providers: make(map[string]OAuthProvider),
	}
}

// Register registers a new OAuth provider
func (r *ProviderRegistry) Register(provider OAuthProvider) error {
	serviceName := provider.GetServiceName()
	if serviceName == "" {
		return fmt.Errorf("provider service name cannot be empty")
	}

	if _, exists := r.providers[serviceName]; exists {
		return fmt.Errorf("provider for service '%s' already registered", serviceName)
	}

	r.providers[serviceName] = provider
	return nil
}

// GetProvider retrieves a provider by service name
func (r *ProviderRegistry) GetProvider(serviceName string) (OAuthProvider, error) {
	provider, exists := r.providers[serviceName]
	if !exists {
		return nil, fmt.Errorf("no provider registered for service '%s'", serviceName)
	}
	return provider, nil
}

// ListProviders returns all registered provider names
func (r *ProviderRegistry) ListProviders() []string {
	var names []string
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}

// GetAvailableProviders returns information about all registered providers
func (r *ProviderRegistry) GetAvailableProviders() map[string]ProviderInfo {
	info := make(map[string]ProviderInfo)
	for name, provider := range r.providers {
		info[name] = ProviderInfo{
			ServiceName:        provider.GetServiceName(),
			DisplayName:        provider.GetDisplayName(),
			SupportsDeviceFlow: provider.SupportsDeviceFlow(),
			SupportsAuthCode:   provider.SupportsAuthorizationCode(),
			DefaultScopes:      provider.GetRequiredScopes(),
		}
	}
	return info
}

// ProviderInfo contains metadata about a provider
type ProviderInfo struct {
	ServiceName        string   `json:"service_name"`
	DisplayName        string   `json:"display_name"`
	SupportsDeviceFlow bool     `json:"supports_device_flow"`
	SupportsAuthCode   bool     `json:"supports_auth_code"`
	DefaultScopes      []string `json:"default_scopes"`
}

// IsExpired checks if a token is expired
func (t *TokenInfo) IsExpired() bool {
	if t.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*t.ExpiresAt)
}

// IsExpiredWithBuffer checks if token expires within the buffer duration
func (t *TokenInfo) IsExpiredWithBuffer(buffer time.Duration) bool {
	if t.ExpiresAt == nil {
		return false
	}
	return time.Now().Add(buffer).After(*t.ExpiresAt)
}

// GetAuthorizationHeader returns the proper authorization header value
func (t *TokenInfo) GetAuthorizationHeader() string {
	if t.TokenType != "" {
		return t.TokenType + " " + t.AccessToken
	}
	return "Bearer " + t.AccessToken
}
