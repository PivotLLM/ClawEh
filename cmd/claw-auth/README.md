# claw-auth

A generic authentication helper for ClawEh that supports multiple providers through a modular architecture.

This is intended for services that do not allow or limit device authentication flows. It implements a desktop oauth2 flow and passes the tokens to ClawEh.

## Features

- **Modular Provider Architecture**: Easy to add new OAuth providers
- **Automatic Flow Selection**: Each provider uses its optimal OAuth flow
- **Secure Token Handling**: Encrypted token storage and transmission
- **Service-Agnostic CLI**: Single command interface for all providers
- **Simple Configuration**: File-based configuration with environment variables
- **MCPFusion Integration**: Direct integration with MCPFusion server

## Supported Providers

- **Google APIs**

## Installation

```bash
# From the MCPFusion project root
cd cmd/fusion-oauth
go build -o fusion-oauth .
```

## Quick Start

1. **List available providers:**
```bash
./fusion-oauth -list
```

2. **Authenticate with Google:**
```bash
./fusion-oauth -service google -fusion http://10.0.0.1:8080 -token <your-mcp-token>
```

3. **Authenticate with GitHub:**
```bash
./fusion-oauth -service github -fusion https://mcp.example.com -token <your-mcp-token>
```

## Command Line Options

```bash
Usage: fusion-oauth [OPTIONS]

Options:
  -service string        OAuth service provider (e.g., google, github, dropbox)
  -fusion string         MCPFusion server URL (e.g., http://10.0.0.1:8080)
  -token string          MCPFusion API token for authentication
  -config string         Configuration file path
  -timeout duration      OAuth flow timeout (default 10m0s)
  -verbose               Enable verbose logging
  -version               Show version information
  -list                  List available OAuth providers
```

## Configuration

### Environment Variables

Set the following environment variables for each service:

**Google:**
```bash
export GOOGLE_CLIENT_ID="your-google-client-id"
export GOOGLE_CLIENT_SECRET="your-google-client-secret"
```

**GitHub:**
```bash
export GITHUB_CLIENT_ID="your-github-client-id"
export GITHUB_CLIENT_SECRET="your-github-client-secret"
```

**Microsoft 365:**
```bash
export MS365_CLIENT_ID="your-ms365-client-id"
export MS365_TENANT_ID="your-ms365-tenant-id"
```

### Configuration File

Create a `config.json` file based on the provided `config.example.json`:

```json
{
  "services": {
    "google": {
      "display_name": "Google APIs",
      "client_id": "${GOOGLE_CLIENT_ID}",
      "client_secret": "${GOOGLE_CLIENT_SECRET}"
    }
  },
  "timeout": "10m"
}
```

Use the configuration file:
```bash
./fusion-oauth -config config.json -service google -fusion http://10.0.0.1:8080 -token <token>
```

## OAuth Flows

Each provider automatically uses its optimal OAuth flow:

### Device Flow
Used by providers like Google for command-line applications:
1. Tool requests device code from provider
2. User visits verification URL and enters user code
3. Tool polls for token completion
4. Tokens are securely transferred to MCPFusion

### Authorization Code Flow
Used by providers like GitHub that require browser-based authentication:
1. Tool opens browser to authorization URL
2. User grants permissions in browser
3. Provider redirects to callback with authorization code
4. Tool exchanges code for tokens
5. Tokens are securely transferred to MCPFusion

## Adding New Providers

To add a new OAuth provider:

1. **Create provider package:**
```bash
mkdir cmd/fusion-oauth/providers/newservice
```

2. **Implement provider interface:**
```go
package newservice

import "github.com/PivotLLM/MCPFusion/cmd/auth/providers"

type Provider struct {
    // Provider-specific fields
}

func NewProvider() *Provider {
    return &Provider{}
}

func (p *Provider) GetServiceName() string {
    return "newservice"
}

// Implement all other interface methods...
```

3. **Register provider in main.go:**
```go
func registerProviders(registry *providers.ProviderRegistry) error {
    // ... existing providers
    
    newServiceProvider := newservice.NewProvider()
    if err := registry.Register(newServiceProvider); err != nil {
        return fmt.Errorf("failed to register newservice provider: %w", err)
    }
    
    return nil
}
```

## Security

- **Token Encryption**: All tokens are encrypted using AES-GCM before transmission
- **Secure Storage**: Tokens are never stored in plaintext on disk
- **Password Derivation**: Uses PBKDF2 for key derivation from passwords
- **Secure Transport**: All communication uses HTTPS/TLS
- **Minimal Permissions**: Requests only necessary OAuth scopes

## Integration with MCPFusion

The tool integrates with MCPFusion through several API endpoints:

- `POST /api/v1/oauth/tokens` - Store OAuth tokens
- `GET /api/v1/auth/verify` - Verify API token
- `GET /api/v1/services/{service}/config` - Get service configuration
- `POST /api/v1/oauth/success` - Success notification
- `POST /api/v1/oauth/error` - Error notification

## Troubleshooting

### Common Issues

1. **Invalid client credentials:**
   - Verify environment variables are set correctly
   - Check OAuth app configuration in provider console

2. **Token storage failures:**
   - Verify MCPFusion server is running
   - Check API token validity
   - Ensure proper network connectivity

3. **Scope permission errors:**
   - Each provider has predefined scopes for comprehensive access
   - Ensure OAuth app has necessary permissions configured in provider console

### Debug Mode

Enable verbose logging for detailed information:
```bash
./fusion-oauth -verbose -service google -fusion http://10.0.0.1:8080 -token <token>
```

## Development

### Testing

```bash
# Run unit tests
go test ./...

# Test specific provider
go test ./providers/google

# Integration test with MCPFusion
go test -integration ./...
```

### Building

```bash
# Build for current platform
go build -o fusion-oauth .

# Build for multiple platforms
GOOS=linux GOARCH=amd64 go build -o fusion-oauth-linux-amd64 .
GOOS=windows GOARCH=amd64 go build -o fusion-oauth-windows-amd64.exe .
GOOS=darwin GOARCH=amd64 go build -o fusion-oauth-darwin-amd64 .
```

## License

Copyright (c) 2025 Tenebris Technologies Inc. All rights reserved.