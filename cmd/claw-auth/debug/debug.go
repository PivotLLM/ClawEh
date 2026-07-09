/******************************************************************************
 * Copyright (c) 2025-2026 Tenebris Technologies Inc.                         *
 * Please see LICENSE file for details.                                       *
 ******************************************************************************/

package debug

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// Debug is a global flag to enable debug logging
var Debug bool

// LogHTTPRequest logs the details of an HTTP request when Debug is true
func LogHTTPRequest(req *http.Request) {
	if !Debug {
		return
	}

	log.Println("=== HTTP REQUEST ===")
	log.Printf("Method: %s", req.Method)
	log.Printf("URL: %s", req.URL.String())

	// Log headers
	log.Println("Headers:")
	for name, values := range req.Header {
		for _, value := range values {
			// Mask Authorization header for security
			if strings.ToLower(name) == "authorization" {
				log.Printf("  %s: %s", name, maskAuthHeader(value))
			} else {
				log.Printf("  %s: %s", name, value)
			}
		}
	}

	// Log body if present
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			log.Printf("Error reading request body: %v", err)
		} else if len(body) > 0 {
			log.Printf("Body: %s", maskSensitiveData(string(body)))
			// Restore the body for actual use
			req.Body = io.NopCloser(bytes.NewReader(body))
		}
	}
	log.Println("==================")
}

// LogHTTPResponse logs the details of an HTTP response when Debug is true
func LogHTTPResponse(resp *http.Response) {
	if !Debug {
		return
	}

	log.Println("=== HTTP RESPONSE ===")
	log.Printf("Status: %s", resp.Status)
	log.Printf("Status Code: %d", resp.StatusCode)

	// Log headers
	log.Println("Headers:")
	for name, values := range resp.Header {
		for _, value := range values {
			log.Printf("  %s: %s", name, value)
		}
	}

	// Log body if present
	if resp.Body != nil {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Printf("Error reading response body: %v", err)
		} else if len(body) > 0 {
			log.Printf("Body: %s", maskSensitiveData(string(body)))
			// Restore the body for actual use
			resp.Body = io.NopCloser(bytes.NewReader(body))
		}
	}
	log.Println("====================")
}

// maskAuthHeader masks sensitive parts of authorization headers
func maskAuthHeader(value string) string {
	parts := strings.Split(value, " ")
	if len(parts) == 2 {
		return fmt.Sprintf("%s %s***", parts[0], parts[1][:min(4, len(parts[1]))])
	}
	return "***"
}

// maskSensitiveData masks sensitive data in request/response bodies
func maskSensitiveData(data string) string {
	sensitive := []string{
		"access_token",
		"refresh_token",
		"client_secret",
		"code",
		"password",
	}

	result := data
	for _, field := range sensitive {
		// Match JSON format: "field":"value"
		result = maskJSONField(result, field)
		// Match form data format: field=value
		result = maskFormField(result, field)
	}

	return result
}

// maskJSONField masks sensitive JSON fields
func maskJSONField(data, field string) string {
	// Pattern: "field":"value"
	pattern := fmt.Sprintf(`"%s":"([^"]*)"`, field)
	return replacePattern(data, pattern)
}

// maskFormField masks sensitive form fields
func maskFormField(data, field string) string {
	// Pattern: field=value (ending with & or end of string)
	pattern := fmt.Sprintf(`%s=([^&\s]*)`, field)
	return replacePattern(data, pattern)
}

// replacePattern is a simple pattern replacement function
// Always replaces sensitive values with "***" for security
func replacePattern(text, pattern string) string {
	// Simple string replacement for sensitive data masking
	// Handle both JSON and form-encoded patterns
	result := text

	// For JSON patterns like "field":"value"
	if strings.Contains(pattern, `":"`) {
		// Extract field name from pattern like "field":"([^"]*)"
		fieldStart := strings.Index(pattern, `"`) + 1
		fieldEnd := strings.Index(pattern[fieldStart:], `"`)
		if fieldEnd > 0 {
			field := pattern[fieldStart : fieldStart+fieldEnd]
			// Look for "field":"anything" and replace with replacement
			jsonPattern := fmt.Sprintf(`"%s":"`, field)
			lines := strings.Split(result, "\n")
			for i, line := range lines {
				if strings.Contains(line, jsonPattern) {
					// Find the start and end of the value
					start := strings.Index(line, jsonPattern) + len(jsonPattern)
					end := strings.Index(line[start:], `"`)
					if end > 0 {
						// Use the actual replacement value (e.g., "field":"***")
						// Extract just the value part from replacement
						lines[i] = line[:start] + "***" + line[start+end:]
					}
				}
			}
			result = strings.Join(lines, "\n")
		}
	} else {
		// For form patterns like field=value
		// Extract field name from pattern
		fieldEnd := strings.Index(pattern, "=")
		if fieldEnd > 0 {
			field := pattern[:fieldEnd]
			lines := strings.Split(result, "\n")
			for i, line := range lines {
				if strings.Contains(line, field+"=") {
					parts := strings.Split(line, "=")
					if len(parts) >= 2 && parts[0] == field {
						lines[i] = parts[0] + "=***"
					}
				}
			}
			result = strings.Join(lines, "\n")
		}
	}

	return result
}
