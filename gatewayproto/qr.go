package gatewayproto

import "encoding/json"

// SetupPayload is the Rabbit R1 QR/setup payload (verified from r1-openclaw.sh).
// The QR encodes this struct as raw JSON (NOT base64url). The legacy "type" string
// "clawdbot-gateway" is what current R1 firmware parses, so it must match exactly.
type SetupPayload struct {
	Type     string   `json:"type"`     // always "clawdbot-gateway"
	Version  int      `json:"version"`  // always 1
	IPs      []string `json:"ips"`      // routable LAN IPv4s; the device tries each
	Port     int      `json:"port"`     // gateway listen port
	Token    string   `json:"token"`    // shared gateway auth token
	Protocol string   `json:"protocol"` // "ws" (LAN) or "wss"
}

// SetupPayloadType / SetupPayloadVersion / SetupPayloadProtocolWS are the fixed
// values the current R1 firmware expects.
const (
	SetupPayloadType    = "clawdbot-gateway"
	SetupPayloadVersion = 1
	SetupProtocolWS     = "ws"
	SetupProtocolWSS    = "wss"
)

// NewSetupPayload builds the R1 setup payload for the given LAN IPs, port, and token.
func NewSetupPayload(ips []string, port int, token, protocol string) SetupPayload {
	if protocol == "" {
		protocol = SetupProtocolWS
	}
	if ips == nil {
		ips = []string{}
	}
	return SetupPayload{
		Type:     SetupPayloadType,
		Version:  SetupPayloadVersion,
		IPs:      ips,
		Port:     port,
		Token:    token,
		Protocol: protocol,
	}
}

// Encode returns the compact JSON string that goes into the QR code.
func (p SetupPayload) Encode() (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
