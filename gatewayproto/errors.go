package gatewayproto

// ErrorCode is the closed top-level error code set used in response frames
// (packages/gateway-protocol/src/schema/error-codes.ts). There is intentionally
// no UNAUTHORIZED/FORBIDDEN/NOT_FOUND/RATE_LIMITED code — auth/role/scope failures
// use InvalidRequest, transient backend failures use Unavailable, and richer
// reasons travel in ErrorShape.Details.
type ErrorCode = string

const (
	CodeNotLinked        ErrorCode = "NOT_LINKED"
	CodeNotPaired        ErrorCode = "NOT_PAIRED"
	CodeAgentTimeout     ErrorCode = "AGENT_TIMEOUT"
	CodeInvalidRequest   ErrorCode = "INVALID_REQUEST"
	CodeApprovalNotFound ErrorCode = "APPROVAL_NOT_FOUND"
	CodeUnavailable      ErrorCode = "UNAVAILABLE"
)

// Connect-time detail codes carried in ErrorShape.Details.code. Subset of the
// reference set (connect-error-details.ts) relevant to a device/node client.
const (
	DetailProtocolMismatch     = "PROTOCOL_MISMATCH"
	DetailAuthRequired         = "AUTH_REQUIRED"
	DetailAuthUnauthorized     = "AUTH_UNAUTHORIZED"
	DetailAuthTokenMismatch    = "AUTH_TOKEN_MISMATCH"
	DetailAuthRateLimited      = "AUTH_RATE_LIMITED"
	DetailDeviceIdentityNeeded = "DEVICE_IDENTITY_REQUIRED"
	DetailDeviceAuthFailed     = "DEVICE_AUTH_FAILED"
	DetailPairingRequired      = "PAIRING_REQUIRED"
)

// ErrorShape is the structured error body of a failed response frame.
type ErrorShape struct {
	Code         ErrorCode `json:"code"`
	Message      string    `json:"message"`
	Details      any       `json:"details,omitempty"`
	Retryable    bool      `json:"retryable,omitempty"`
	RetryAfterMs int       `json:"retryAfterMs,omitempty"`
}

// NewError builds an ErrorShape with an optional details payload.
func NewError(code ErrorCode, message string, details any) *ErrorShape {
	return &ErrorShape{Code: code, Message: message, Details: details}
}
