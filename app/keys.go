// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package app

// The minisign public keys a ClawEh release may be signed with. `claw upgrade`
// refuses to install anything whose checksums.txt is not signed by one of
// them, so they are the root of trust for upgrades, and they live here beside
// the version because both are properties of a release, not of the machine
// that built it.
//
// Two slots so a key can be rolled without stranding installed copies:
//
//	release N:   both keys embedded; sign with releaseKey1
//	release N+1: sign with releaseKey2 (every N install already trusts it)
//	release N+2: generate a new key, put it in releaseKey1; releaseKey2 is now current
//
// A copy older than N does not trust releaseKey2, so it must upgrade to N (or
// any release still signed with the key it knows) first. Keep at least one
// release signed with the old key downloadable until every install has moved.
//
// Each value is the second line of a minisign.pub file (base64, 56 characters,
// starting "RW"). An empty slot is ignored; with both empty upgrading fails
// closed. The secret keys stay in ~/.minisign on the release machine;
// `make release-sign` signs with ~/.minisign/minisign.key.
const (
	releaseKey1 = "RWS4LMzQBg4PUnmdFHlaHFkUt+mm9PLPGvkE812t8Ew4YK5fL8zWqbuN"
	releaseKey2 = "RWRGKiCpxVbnTDuMRnilq64GiBeQKnxIVuEBN0KgDQ4ry91j3idlaLdz"
)

// ReleasePublicKeys returns the minisign public keys a release may be signed
// with, in slot order. Empty slots are included; the verifier skips them.
func ReleasePublicKeys() []string {
	return []string{releaseKey1, releaseKey2}
}
