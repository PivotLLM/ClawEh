package config

import (
	"fmt"
	"net/netip"
	"strings"
)

// LockoutExemptSet is a compiled gateway.lockout_exempt: the client addresses
// no per-address lockout applies to. Loopback is always in it. It is
// immutable once built, so a reload can swap in a new one. A nil set exempts
// loopback only.
type LockoutExemptSet struct {
	prefixes []netip.Prefix
}

// CompileLockoutExempt parses entries, each an IP address or a CIDR. A bare
// address is a single-address prefix (/32 or /128).
func CompileLockoutExempt(entries []string) (*LockoutExemptSet, error) {
	prefixes, err := compileAddrPrefixes("gateway.lockout_exempt", entries)
	if err != nil {
		return nil, err
	}
	return &LockoutExemptSet{prefixes: prefixes}, nil
}

// compileAddrPrefixes parses entries, each an IP address or a CIDR, for the
// config key named by key. A bare address is a single-address prefix.
func compileAddrPrefixes(key string, entries []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(entries))
	for _, entry := range entries {
		p, err := parseExemptEntry(strings.TrimSpace(entry))
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not an IP address or CIDR", key, entry)
		}
		prefixes = append(prefixes, p)
	}
	return prefixes, nil
}

func parseExemptEntry(entry string) (netip.Prefix, error) {
	if strings.Contains(entry, "/") {
		p, err := netip.ParsePrefix(entry)
		if err != nil {
			return netip.Prefix{}, err
		}
		return p.Masked(), nil
	}
	addr, err := netip.ParseAddr(entry)
	if err != nil {
		return netip.Prefix{}, err
	}
	if addr.Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("zoned address %q", entry)
	}
	addr = addr.Unmap()
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// Contains reports whether ip (an address without a port) is exempt. Loopback
// always is; an unparseable ip never is.
func (s *LockoutExemptSet) Contains(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.WithZone("").Unmap()
	if addr.IsLoopback() {
		return true
	}
	if s == nil {
		return false
	}
	for _, p := range s.prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
