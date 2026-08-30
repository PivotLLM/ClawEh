package gatewayproto

// ClientInfo is the client block of ConnectParams.
type ClientInfo struct {
	ID              string `json:"id"`
	DisplayName     string `json:"displayName,omitempty"`
	Version         string `json:"version"`
	Platform        string `json:"platform"`
	DeviceFamily    string `json:"deviceFamily,omitempty"`
	ModelIdentifier string `json:"modelIdentifier,omitempty"`
	Mode            string `json:"mode"`
	InstanceID      string `json:"instanceId,omitempty"`
}

// DeviceIdentity is the device block of ConnectParams (Ed25519 identity + signed nonce).
type DeviceIdentity struct {
	ID        string `json:"id"`
	PublicKey string `json:"publicKey"`
	Signature string `json:"signature"`
	SignedAt  int64  `json:"signedAt"`
	Nonce     string `json:"nonce"`
}

// ConnectAuth carries the credential family presented at connect.
type ConnectAuth struct {
	Token          string `json:"token,omitempty"`
	BootstrapToken string `json:"bootstrapToken,omitempty"`
	DeviceToken    string `json:"deviceToken,omitempty"`
	Password       string `json:"password,omitempty"`
}

// ConnectParams is the params of the first "connect" request frame.
type ConnectParams struct {
	MinProtocol int             `json:"minProtocol"`
	MaxProtocol int             `json:"maxProtocol"`
	Client      ClientInfo      `json:"client"`
	Caps        []string        `json:"caps,omitempty"`
	Commands    []string        `json:"commands,omitempty"`
	Permissions map[string]bool `json:"permissions,omitempty"`
	Role        string          `json:"role,omitempty"`
	Scopes      []string        `json:"scopes,omitempty"`
	Device      *DeviceIdentity `json:"device,omitempty"`
	Auth        *ConnectAuth    `json:"auth,omitempty"`
	Locale      string          `json:"locale,omitempty"`
	UserAgent   string          `json:"userAgent,omitempty"`
}

// ChallengePayload is the payload of the connect.challenge event sent on open.
type ChallengePayload struct {
	Nonce string `json:"nonce"`
	Ts    int64  `json:"ts"`
}

// EventConnectChallenge is the event name of the pre-auth challenge.
const EventConnectChallenge = "connect.challenge"

// HelloOk is the payload of the successful connect response (inside a res frame).
type HelloOk struct {
	Type     string        `json:"type"` // always "hello-ok"
	Protocol int           `json:"protocol"`
	Server   HelloServer   `json:"server"`
	Features HelloFeatures `json:"features"`
	Auth     HelloAuth     `json:"auth"`
	Policy   HelloPolicy   `json:"policy"`
}

type HelloServer struct {
	Version string `json:"version"`
	ConnID  string `json:"connId"`
}

type HelloFeatures struct {
	Methods []string `json:"methods"`
	Events  []string `json:"events"`
}

// HelloDeviceToken is one issued device token (one per granted role).
type HelloDeviceToken struct {
	DeviceToken string   `json:"deviceToken"`
	Role        string   `json:"role"`
	Scopes      []string `json:"scopes"`
	IssuedAtMs  int64    `json:"issuedAtMs"`
}

type HelloAuth struct {
	DeviceToken  string             `json:"deviceToken,omitempty"`
	Role         string             `json:"role"`
	Scopes       []string           `json:"scopes"`
	IssuedAtMs   int64              `json:"issuedAtMs,omitempty"`
	DeviceTokens []HelloDeviceToken `json:"deviceTokens,omitempty"`
}

type HelloPolicy struct {
	MaxPayload       int `json:"maxPayload"`
	MaxBufferedBytes int `json:"maxBufferedBytes"`
	TickIntervalMs   int `json:"tickIntervalMs"`
}

// Policy limits advertised in hello-ok (server-constants.ts).
const (
	MaxPayloadBytes        = 25 * 1024 * 1024
	MaxBufferedBytes       = 50 * 1024 * 1024
	MaxPreauthPayloadBytes = 64 * 1024
	TickIntervalMs         = 30_000
)

// Client modes (client-info.ts GATEWAY_CLIENT_MODES).
const (
	ModeNode    = "node"
	ModeUI      = "ui"
	ModeBackend = "backend"
)

// Roles (role-policy.ts).
const (
	RoleOperator = "operator"
	RoleNode     = "node"
)
