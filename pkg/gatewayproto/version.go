package gatewayproto

// Protocol version constants. ProtocolVersion is the highest we speak; we accept
// down to MinClientProtocolVersion. The OpenClaw reference currently pins its
// constants at 4, but the protocol version is a compatibility marker with no
// wire-shape branching in the gateway, and the Rabbit R1 (firmware 20260619.1)
// advertises protocol 3 — so ClawEh negotiates the range [3, 4] and echoes the
// negotiated version back in hello-ok.
const (
	ProtocolVersion          = 4
	MinClientProtocolVersion = 3
	MinProbeProtocolVersion  = 3
)

// NegotiateProtocol returns the highest protocol version both sides support, or 0
// if there is no overlap. ClawEh speaks [MinClientProtocolVersion, ProtocolVersion]
// (probe clients relax the floor to MinProbeProtocolVersion).
func NegotiateProtocol(clientMin, clientMax int, probe bool) int {
	ourMin := MinClientProtocolVersion
	if probe && MinProbeProtocolVersion < ourMin {
		ourMin = MinProbeProtocolVersion
	}
	negotiated := clientMax
	if negotiated > ProtocolVersion {
		negotiated = ProtocolVersion
	}
	if negotiated < clientMin || negotiated < ourMin {
		return 0
	}
	return negotiated
}
