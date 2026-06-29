package gatewayproto

// Protocol version constants. The OpenClaw reference server currently pins all
// three at 4 (packages/gateway-protocol/src/version.ts). A client is accepted
// when maxProtocol >= ProtocolVersion && minProtocol <= ProtocolVersion (probe
// clients relax the lower bound to MinProbeProtocolVersion).
const (
	ProtocolVersion          = 4
	MinClientProtocolVersion = 4
	MinProbeProtocolVersion  = 4
)

// NegotiateProtocol reports whether a client advertising [minProtocol, maxProtocol]
// is compatible. probe relaxes the accepted floor (probe-mode connections only).
func NegotiateProtocol(minProtocol, maxProtocol int, probe bool) bool {
	floor := ProtocolVersion
	if probe {
		floor = MinProbeProtocolVersion
	}
	return maxProtocol >= floor && minProtocol <= ProtocolVersion
}
