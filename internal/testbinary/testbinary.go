// ClawEh
// License: MIT

// Package testbinary sets the name app.BinaryName reports, for tests that
// assert text containing it. app.BinaryName reads os.Args[0], which under
// `go test` is the test binary's name; Set replaces it for the test's
// lifetime. Only tests import this package.
package testbinary

import (
	"os"
	"path/filepath"
	"testing"
)

// Set makes app.BinaryName return name until the test ends. It must not be
// used in parallel tests: os.Args is process-wide.
func Set(tb testing.TB, name string) {
	tb.Helper()
	orig := os.Args
	tb.Cleanup(func() { os.Args = orig })
	args := []string{filepath.Join(string(filepath.Separator)+"usr", "local", "bin", name)}
	if len(orig) > 1 {
		args = append(args, orig[1:]...)
	}
	os.Args = args
}
