package config

import "testing"

func TestBackupConfig_EnabledByDefault(t *testing.T) {
	// Field absent (nil) → on by default.
	if !(BackupConfig{}).IsEnabled() {
		t.Error("backup must be enabled by default when unset")
	}
	no := false
	if (BackupConfig{Enabled: &no}).IsEnabled() {
		t.Error("explicit enabled:false must disable backup")
	}
	yes := true
	if !(BackupConfig{Enabled: &yes}).IsEnabled() {
		t.Error("explicit enabled:true must enable backup")
	}
}

func TestBackupConfigDefaults(t *testing.T) {
	// Unset → defaults 03:00 / 30 days.
	var b BackupConfig
	if h, m := b.BackupAt(); h != 3 || m != 0 {
		t.Errorf("default BackupAt = %02d:%02d, want 03:00", h, m)
	}
	if b.BackupRetainDays() != 30 {
		t.Errorf("default retain = %d, want 30", b.BackupRetainDays())
	}
	// Explicit values honored.
	b = BackupConfig{At: "23:45", RetainDays: 7}
	if h, m := b.BackupAt(); h != 23 || m != 45 {
		t.Errorf("BackupAt = %02d:%02d, want 23:45", h, m)
	}
	if b.BackupRetainDays() != 7 {
		t.Errorf("retain = %d, want 7", b.BackupRetainDays())
	}
	// Garbage time falls back to default.
	if h, m := (BackupConfig{At: "nonsense"}).BackupAt(); h != 3 || m != 0 {
		t.Errorf("bad time should default to 03:00, got %02d:%02d", h, m)
	}
}
