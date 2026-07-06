/******************************************************************************
 * Copyright (c) 2025-2026 Tenebris Technologies Inc.                         *
 * Please see LICENSE file for details.                                       *
 ******************************************************************************/

package config

// GetServiceConfigs returns default service configurations.
// These are fallback defaults only — claw-auth fetches the actual OAuth config
// (client ID, secret, scopes) from the MCPFusion server at runtime.
func GetServiceConfigs() map[string]*ServiceConfig {
	return map[string]*ServiceConfig{
		"google": {
			DisplayName: "Google",
			Endpoints: &EndpointConfig{
				AuthorizationURL: "https://accounts.google.com/o/oauth2/v2/auth",
				TokenURL:         "https://oauth2.googleapis.com/token",
			},
			Metadata: map[string]string{
				"description": "Access to Google services including Calendar, Gmail, and Drive",
			},
		},
		"github": {
			DisplayName: "GitHub",
			Metadata: map[string]string{
				"description": "Access to GitHub repositories and user information",
			},
		},
		"microsoft365": {
			DisplayName: "Microsoft 365",
			Endpoints: &EndpointConfig{
				AuthorizationURL: "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
				TokenURL:         "https://login.microsoftonline.com/common/oauth2/v2.0/token",
				DeviceCodeURL:    "https://login.microsoftonline.com/common/oauth2/v2.0/devicecode",
			},
			Metadata: map[string]string{
				"description": "Access to Microsoft 365 services including Outlook, OneDrive, and Teams",
			},
		},
	}
}
