// ClawEh
// License: MIT

package config

// newProblems returns, as errors, the problems in next whose key is not among
// before's. Store.Update refuses only these: a problem already in the file
// (left by an older release or a hand edit) must not block an unrelated save,
// including the one that fixes it. toErr returns nil for a problem that is
// never refused.
func newProblems[P any, K comparable](before, next []P, key func(P) K, toErr func(P) error) []error {
	had := make(map[K]bool, len(before))
	for _, p := range before {
		had[key(p)] = true
	}
	var errs []error
	for _, p := range next {
		if had[key(p)] {
			continue
		}
		if err := toErr(p); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
