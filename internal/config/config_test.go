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

func TestLoadDefaultsToRestoreAndRequiresConfig(t *testing.T) {
	if _, err := LoadFromMap(map[string]string{}); err == nil {
		t.Fatalf("LoadFromMap succeeded without restore config")
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
	_, err := LoadFromMap(env)
	if err == nil {
		t.Fatalf("LoadFromMap succeeded for unsupported multipart URLs")
	}
}

func TestLoadRejectsInvalidInteger(t *testing.T) {
	env := validEnv()
	env["DOWNLOAD_CONCURRENCY"] = "sixteen"
	if _, err := LoadFromMap(env); err == nil {
		t.Fatalf("LoadFromMap succeeded with invalid integer")
	}
}

func TestLoadRejectsInvalidBoolean(t *testing.T) {
	env := validEnv()
	env["WIPE_EXISTING"] = "maybe"
	if _, err := LoadFromMap(env); err == nil {
		t.Fatalf("LoadFromMap succeeded with invalid boolean")
	}
}

func TestParseBytesSupportsTiB(t *testing.T) {
	got, err := ParseBytes("5TiB")
	if err != nil {
		t.Fatalf("ParseBytes returned error: %v", err)
	}
	if got != 5*1024*1024*1024*1024 {
		t.Fatalf("ParseBytes = %d, want 5 TiB", got)
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

func TestLoadStripComponents(t *testing.T) {
	env := validEnv()
	env["STRIP_COMPONENTS"] = "3"
	cfg, err := LoadFromMap(env)
	if err != nil {
		t.Fatalf("LoadFromMap returned error: %v", err)
	}
	if cfg.StripComponents != 3 {
		t.Fatalf("StripComponents = %d, want 3", cfg.StripComponents)
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
