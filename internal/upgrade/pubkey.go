package upgrade

// releasePublicKeys are the minisign public keys a ClawEh release may be
// signed with. `claw upgrade` refuses to install anything whose checksums.txt
// is not signed by one of them, so they are the root of trust for upgrades:
// at least one must be set before the first release that ships this code, and
// a release signed with any other key will not install.
//
// Two slots so a key can be rolled without stranding installed copies:
//
//	release N:   embed the current key AND the next key; sign with the current key
//	release N+1: sign with the next key (every N install already trusts it)
//	release N+2: drop the old key from this list
//
// A copy older than N does not trust the next key, so it must upgrade to N
// (or any release still signed with the old key) first. Keep at least one
// release signed with the old key available until every install has moved.
//
// Value of each slot: the second line of the maintainer's minisign.pub file
// (the base64 blob, 56 characters, starting "RW"), or the whole file. An empty
// slot is ignored; with every slot empty upgrading fails closed with
// errNoReleaseKey.
//
// Release process (maintainer's machine):
//
//	minisign -G                       # once per key; writes ~/.minisign/minisign.key and ./minisign.pub
//	make release-sign                 # signs with ~/.minisign/minisign.key (or MINISIGN_KEY=<path>)
//	# upload build/*.tar.gz, build/*.sha256, build/checksums.txt, build/checksums.txt.minisig, build/sbom.json
var releasePublicKeys = []string{
	"", // current key
	"", // next key, for rotation
}
