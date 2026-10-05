package gateway

import (
	"testing"

	"github.com/PivotLLM/ClawEh/internal/backup"
)

// The restore refuses to run while the gateway holds its lock; both packages
// must agree on the file it is held on.
func TestLockFileNameMatchesBackupPackage(t *testing.T) {
	if lockFileName != backup.LockFileName {
		t.Fatalf("gateway lock %q != backup.LockFileName %q", lockFileName, backup.LockFileName)
	}
}
