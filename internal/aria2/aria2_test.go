package aria2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteInputFileUsesDeterministicNames(t *testing.T) {
	tmp := t.TempDir()
	path, err := WriteInputFile(tmp, []string{"https://example.invalid/a.part0000", "https://example.invalid/a.part0001"})
	if err != nil {
		t.Fatalf("WriteInputFile error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "out=part-000000") || !strings.Contains(text, "out=part-000001") {
		t.Fatalf("input file missing deterministic outputs:\n%s", text)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	if filepath.Dir(path) != tmp {
		t.Fatalf("input file dir = %q", filepath.Dir(path))
	}
}
