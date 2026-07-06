/******************************************************************************
 * Copyright (c) 2025-2026 Tenebris Technologies Inc.                         *
 * Please see LICENSE file for details.                                       *
 ******************************************************************************/

package config

import (
	"fmt"
	"time"
)

// Config holds the configuration for the fusion-oauth tool
type Config struct {
	// Command-line options
	Service   string        `json:"service"`
	FusionURL string        `json:"fusion_url"`
	APIToken  string        `json:"api_token,omitempty"` // Don't persist sensitive data
	Timeout   time.Duration `json:"timeout"`
	Verbose   bool          `json:"verbose"`

	// OAuth configuration (loaded from config file only)
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	TenantID     string `json:"tenant_id,omitempty"` // For Microsoft 365
	RedirectURI  string `json:"redirect_uri,omitempty"`

	// Service-specific configurations
	Services map[string]*ServiceConfig `json:"services,omitempty"`

	// Runtime configuration
	ConfigFile string `json:"-"` // Don't serialize
}

// ServiceConfig holds OAuth configuration for a specific service
type ServiceConfig struct {
	DisplayName  string            `json:"display_name,omitempty"`
	ClientID     string            `json:"client_id"`
	ClientSecret string            `json:"client_secret,omitempty"`
	TenantID     string            `json:"tenant_id,omitempty"`
	RedirectURI  string            `json:"redirect_uri,omitempty"`
	Endpoints    *EndpointConfig   `json:"endpoints,omitempty"`
	Scope        string            `json:"scope,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// EndpointConfig allows overriding default OAuth endpoints
type EndpointConfig struct {
	AuthorizationURL string `json:"authorization_url,omitempty"`
	TokenURL         string `json:"token_url,omitempty"`
	DeviceCodeURL    string `json:"device_code_url,omitempty"`
	UserInfoURL      string `json:"user_info_url,omitempty"`
}

// GetServiceConfig returns the configuration for a specific service
func (c *Config) GetServiceConfig(serviceName string) *ServiceConfig {
	if c.Services == nil {
		return nil
	}
	return c.Services[serviceName]
}

// SetServiceConfig sets the configuration for a specific service
func (c *Config) SetServiceConfig(serviceName string, config *ServiceConfig) {
	if c.Services == nil {
		c.Services = make(map[string]*ServiceConfig)
	}
	c.Services[serviceName] = config
}

// MergeServiceConfig merges service-specific config with global config
func (c *Config) MergeServiceConfig(serviceName string) *ServiceConfig {
	serviceConfig := c.GetServiceConfig(serviceName)
	if serviceConfig == nil {
		serviceConfig = &ServiceConfig{}
	}

	// Create a copy to avoid modifying the original
	merged := &ServiceConfig{
		DisplayName:  serviceConfig.DisplayName,
		ClientID:     serviceConfig.ClientID,
		ClientSecret: serviceConfig.ClientSecret,
		TenantID:     serviceConfig.TenantID,
		RedirectURI:  serviceConfig.RedirectURI,
		Endpoints:    serviceConfig.Endpoints,
		Scope:        serviceConfig.Scope,
		Metadata:     serviceConfig.Metadata,
	}

	// Override with global config if service-specific values are empty
	if merged.ClientID == "" {
		merged.ClientID = c.ClientID
	}
	if merged.ClientSecret == "" {
		merged.ClientSecret = c.ClientSecret
	}
	if merged.TenantID == "" {
		merged.TenantID = c.TenantID
	}
	if merged.RedirectURI == "" {
		merged.RedirectURI = c.RedirectURI
	}

	return merged
}

// Validate validates the configuration
func (c *Config) Validate() error {
	if c.Service == "" {
		return fmt.Errorf("service is required")
	}

	if c.FusionURL == "" {
		return fmt.Errorf("fusion URL is required")
	}

	if c.APIToken == "" {
		return fmt.Errorf("API token is required")
	}

	if c.Timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}

	return nil
}

// LoadServiceDefaults loads default service configurations if not already present
func (c *Config) LoadServiceDefaults() {
	if c.Services == nil {
		c.Services = make(map[string]*ServiceConfig)
	}

	defaults := GetServiceConfigs()
	for name, defaultConfig := range defaults {
		if _, exists := c.Services[name]; !exists {
			c.Services[name] = defaultConfig
		}
	}
}
