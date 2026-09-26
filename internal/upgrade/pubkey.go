package upgrade

// releasePublicKey is the minisign public key that every ClawEh release is
// signed with. `claw upgrade` refuses to install anything whose checksums.txt
// is not signed by this key, so the key is the root of trust for upgrades: it
// must be set before the first release that ships this code, and a release
// signed with any other key will not install.
//
// Value: the second line of the maintainer's minisign.pub file (the base64
// blob, 56 characters, starting "RW"), or the whole file. Empty means "no key",
// and upgrading then fails closed with errNoReleaseKey.
//
// Release process (maintainer's machine):
//
//	minisign -G -p minisign.pub -s ~/.minisign/claweh.key    # once, keep the .key offline-safe
//	make release-checksums release-sign MINISIGN_KEY=~/.minisign/claweh.key
//	# upload build/*.tar.gz, build/*.sha256, build/checksums.txt, build/checksums.txt.minisig, build/sbom.json
//
// Rotation: embed the new public key here, cut a release signed with the OLD
// key so existing installs can upgrade to it, then sign every later release
// with the new key.
const releasePublicKey = ""
