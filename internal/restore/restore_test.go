package restore

import (
	"os"
	"path/filepath"
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
