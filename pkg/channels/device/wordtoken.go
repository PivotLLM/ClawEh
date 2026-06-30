package device

import (
	"crypto/rand"
	_ "embed"
	"fmt"
	"math/big"
	"strings"
)

// bip39Raw is the standard BIP39 English wordlist (2048 unambiguous words),
// embedded as a build-time asset. Used to mint human-typeable passphrase tokens.
//
//go:embed assets/bip39-english.txt
var bip39Raw string

var bip39Words = strings.Fields(bip39Raw)

// wordTokenWordCount is the passphrase length. 5 words from the 2048-word list is
// ~55 bits of entropy — typeable, and strong enough as a pre-approval gate given
// that every device still requires cryptographic pairing approval behind it.
const wordTokenWordCount = 5

// GenerateWordToken returns a hyphen-joined 5-word BIP39 passphrase (e.g.
// "anchor-velvet-puzzle-ranger-cobalt"). It is accepted as a shared token by the
// device gateway alongside the long QR token, giving humans a value they can type
// into a client's token field.
func GenerateWordToken() (string, error) {
	if len(bip39Words) == 0 {
		return "", fmt.Errorf("device: empty word list")
	}
	n := big.NewInt(int64(len(bip39Words)))
	words := make([]string, wordTokenWordCount)
	for i := range words {
		idx, err := rand.Int(rand.Reader, n)
		if err != nil {
			return "", fmt.Errorf("device: word token: %w", err)
		}
		words[i] = bip39Words[idx.Int64()]
	}
	return strings.Join(words, "-"), nil
}
