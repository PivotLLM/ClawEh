// ClawEh
// License: MIT

// Package faulttest holds end-to-end fault-injection tests: the real agent
// loop, provider dispatcher, fallback chain and cooldown tracker driven
// against in-process stub providers (internal/test/stubprovider) that fail on
// cue. It has no production code; see the _test.go files.
package faulttest
