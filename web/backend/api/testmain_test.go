package api_test

import (
	"os"
	"testing"

	"github.com/PivotLLM/ClawEh/internal/gateway"
)

func TestMain(m *testing.M) {
	gateway.RegisterToolProvidersForTest()
	os.Exit(m.Run())
}
