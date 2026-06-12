package extract

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractSafeFile(t *testing.T) {
	tmp := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "chaindata/file.txt", Mode: 0644, Size: 5})
	_, _ = tw.Write([]byte("hello"))
	_ = tw.Close()
	if err := ExtractTar(&buf, tmp, Limits{}); err != nil {
		t.Fatalf("ExtractTar error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(tmp, "chaindata/file.txt"))
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("file = %q", got)
	}
}

func TestExtractStripsPathComponents(t *testing.T) {
	tmp := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "a/b/c/chaindata/file.txt", Mode: 0644, Size: 5})
	_, _ = tw.Write([]byte("hello"))
	_ = tw.Close()
	if err := ExtractTar(&buf, tmp, Limits{StripComponents: 3}); err != nil {
		t.Fatalf("ExtractTar error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(tmp, "chaindata/file.txt"))
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("file = %q", got)
	}
	if _, err := os.Stat(filepath.Join(tmp, "a")); !os.IsNotExist(err) {
		t.Fatalf("unstripped parent exists or stat failed: %v", err)
	}
}

func TestExtractStripSkipsTopLevelDirectory(t *testing.T) {
	tmp := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "a/b/c", Typeflag: tar.TypeDir})
	_ = tw.WriteHeader(&tar.Header{Name: "a/b/c/file.txt", Mode: 0644, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	if err := ExtractTar(&buf, tmp, Limits{StripComponents: 3}); err != nil {
		t.Fatalf("ExtractTar error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "file.txt")); err != nil {
		t.Fatalf("stripped file missing: %v", err)
	}
}

func TestRejectTraversal(t *testing.T) {
	tmp := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0644, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	if err := ExtractTar(&buf, tmp, Limits{}); err == nil {
		t.Fatalf("ExtractTar succeeded, want traversal error")
	}
}

func TestRejectAbsolutePath(t *testing.T) {
	tmp := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "/abs", Mode: 0644, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	if err := ExtractTar(&buf, tmp, Limits{}); err == nil {
		t.Fatalf("ExtractTar succeeded, want absolute path error")
	}
}

func TestRejectSymlinkEscape(t *testing.T) {
	tmp := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	_ = tw.Close()
	if err := ExtractTar(&buf, tmp, Limits{}); err == nil {
		t.Fatalf("ExtractTar succeeded, want symlink error")
	}
}

func TestRejectSpecialFile(t *testing.T) {
	tmp := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "pipe", Typeflag: tar.TypeFifo})
	_ = tw.Close()
	if err := ExtractTar(&buf, tmp, Limits{}); err == nil {
		t.Fatalf("ExtractTar succeeded, want special file error")
	}
}

func TestExtractClampsFileMode(t *testing.T) {
	tmp := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "open", Mode: 0777, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	if err := ExtractTar(&buf, tmp, Limits{}); err != nil {
		t.Fatalf("ExtractTar error: %v", err)
	}
	info, err := os.Stat(filepath.Join(tmp, "open"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0644 {
		t.Fatalf("mode = %o, want 0644", got)
	}
}
