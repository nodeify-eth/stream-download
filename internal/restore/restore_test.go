package restore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStampMatchesIdentity(t *testing.T) {
	tmp := t.TempDir()
	stamp := Stamp{SnapshotID: "sha256:abc", Target: "/data", ToolVersion: "test"}
	path := filepath.Join(tmp, ".stream-download.stamp")
	if err := WriteStamp(path, stamp); err != nil {
		t.Fatalf("WriteStamp error: %v", err)
	}
	if ok := StampMatches(path, stamp); !ok {
		t.Fatalf("StampMatches = false, want true")
	}
	stamp.SnapshotID = "sha256:other"
	if ok := StampMatches(path, stamp); ok {
		t.Fatalf("StampMatches = true for changed identity")
	}
}

func TestWriteStampRedactsSourceSecretsAndUsesPrivateMode(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".stream-download.stamp")
	stamp := Stamp{Source: "https://example.invalid/snapshot?X-Amz-Signature=secret&token=abc"}
	if err := WriteStamp(path, stamp); err != nil {
		t.Fatalf("WriteStamp error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "abc") {
		t.Fatalf("stamp leaked secret: %s", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("stamp mode = %o, want 0600", got)
	}
	var got Stamp
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("stamp is not json: %v", err)
	}
	if !strings.Contains(got.Source, "[REDACTED]") {
		t.Fatalf("stamp source was not redacted: %q", got.Source)
	}
}

func TestPrepareTargetRequiresEmptyWithoutWipe(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "data")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "existing"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareTarget(target, false); err == nil {
		t.Fatalf("PrepareTarget succeeded, want non-empty error")
	}
}

func TestPrepareTargetAllowsFreshFilesystemMetadata(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "data")
	if err := os.MkdirAll(filepath.Join(target, "lost+found"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := PrepareTarget(target, false); err != nil {
		t.Fatalf("PrepareTarget error: %v", err)
	}
}

func TestPrepareTargetAllowsStampOnlyTarget(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "data")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".stream-download.stamp"), []byte("{}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareTarget(target, false); err != nil {
		t.Fatalf("PrepareTarget error: %v", err)
	}
}

func TestPrepareTargetAllowsStagingOnlyTarget(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "data")
	if err := os.MkdirAll(filepath.Join(target, StagingDirName, "partial"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := PrepareTarget(target, false); err != nil {
		t.Fatalf("PrepareTarget error: %v", err)
	}
}

func TestCleanupInterruptedPublishRemovesRecordedEntries(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "data")
	if err := os.MkdirAll(filepath.Join(target, "db"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "db", "file"), []byte("partial"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "unrelated"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WritePublishMarker(target, []string{"db"}); err != nil {
		t.Fatalf("WritePublishMarker error: %v", err)
	}
	if err := CleanupInterruptedPublish(target); err != nil {
		t.Fatalf("CleanupInterruptedPublish error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "db")); !os.IsNotExist(err) {
		t.Fatalf("published entry still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "unrelated")); err != nil {
		t.Fatalf("unrelated entry missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, PublishFileName)); !os.IsNotExist(err) {
		t.Fatalf("publish marker still exists: %v", err)
	}
}

func TestPrepareTargetWipeExisting(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "data")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "existing"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareTarget(target, true); err != nil {
		t.Fatalf("PrepareTarget error: %v", err)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("target entries = %d, want 0", len(entries))
	}
}

func TestPrepareTargetWipePreservesLostFound(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "data")
	if err := os.MkdirAll(filepath.Join(target, "lost+found"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "existing"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareTarget(target, true); err != nil {
		t.Fatalf("PrepareTarget error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "lost+found")); err != nil {
		t.Fatalf("lost+found was not preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "existing")); !os.IsNotExist(err) {
		t.Fatalf("existing file after wipe: %v", err)
	}
}
