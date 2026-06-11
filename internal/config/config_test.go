package config

import "testing"

func TestLoadRestoreFalseSkipsWithoutTargetValidation(t *testing.T) {
	env := map[string]string{"RESTORE_SNAPSHOT": "false"}
	cfg, err := LoadFromMap(env)
	if err != nil {
		t.Fatalf("LoadFromMap returned error: %v", err)
	}
	if cfg.RestoreSnapshot {
		t.Fatalf("RestoreSnapshot = true, want false")
	}
}

func TestLoadRejectsUnsafeSubpath(t *testing.T) {
	env := validEnv()
	env["SUBPATH"] = "../escape"
	_, err := LoadFromMap(env)
	if err == nil {
		t.Fatalf("LoadFromMap succeeded, want unsafe SUBPATH error")
	}
}

func TestParseBytes(t *testing.T) {
	tests := map[string]int64{
		"256MiB": 268435456,
		"8GiB":   8589934592,
		"1024":   1024,
	}
	for input, want := range tests {
		got, err := ParseBytes(input)
		if err != nil {
			t.Fatalf("ParseBytes(%q) error: %v", input, err)
		}
		if got != want {
			t.Fatalf("ParseBytes(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestSnapshotURLsTakePrecedence(t *testing.T) {
	env := validEnv()
	env["SNAPSHOT_URL"] = "https://example.invalid/one.tar.zst"
	env["SNAPSHOT_URLS"] = "https://example.invalid/a.part0000,https://example.invalid/a.part0001"
	cfg, err := LoadFromMap(env)
	if err != nil {
		t.Fatalf("LoadFromMap returned error: %v", err)
	}
	if len(cfg.SnapshotURLs) != 2 {
		t.Fatalf("SnapshotURLs len = %d, want 2", len(cfg.SnapshotURLs))
	}
}

func TestLoadDefaultsToTextLogs(t *testing.T) {
	cfg, err := LoadFromMap(validEnv())
	if err != nil {
		t.Fatalf("LoadFromMap returned error: %v", err)
	}
	if cfg.LogFormat != "text" {
		t.Fatalf("LogFormat = %q, want text", cfg.LogFormat)
	}
}

func TestLoadRejectsAmbiguousHTTPAndS3Config(t *testing.T) {
	env := validEnv()
	env["S3_BUCKET"] = "snapshots"
	env["S3_KEY"] = "snapshot.tar.zst"
	_, err := LoadFromMap(env)
	if err == nil {
		t.Fatalf("LoadFromMap succeeded, want ambiguous source error")
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"RESTORE_SNAPSHOT": "true",
		"DIR":              "/data",
		"SCRATCH_DIR":      "/scratch",
		"SNAPSHOT_URL":     "https://example.invalid/snapshot.tar.zst",
	}
}
