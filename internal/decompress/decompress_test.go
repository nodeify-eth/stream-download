package decompress

import (
	"bytes"
	"compress/gzip"
	"io"
	"os/exec"
	"testing"
)

func TestGzipReader(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, _ = gw.Write([]byte("hello"))
	_ = gw.Close()
	rc, err := NewReader("gzip", &buf)
	if err != nil {
		t.Fatalf("NewReader error: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestNoneReader(t *testing.T) {
	rc, err := NewReader("none", bytes.NewBufferString("plain"))
	if err != nil {
		t.Fatalf("NewReader error: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "plain" {
		t.Fatalf("got %q", got)
	}
}

func TestCommandForExternalCompression(t *testing.T) {
	tests := map[string][]string{
		"zstd": {"zstd", "-dc"},
		"zst":  {"zstd", "-dc"},
		"lz4":  {"lz4", "-dc"},
		"xz":   {"xz", "-dc"},
	}
	for kind, want := range tests {
		got, err := CommandFor(kind)
		if err != nil {
			t.Fatalf("CommandFor(%q) error: %v", kind, err)
		}
		if len(got) != len(want) {
			t.Fatalf("CommandFor(%q) = %#v, want %#v", kind, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("CommandFor(%q) = %#v, want %#v", kind, got, want)
			}
		}
	}
}

func TestZstdReaderClosesCleanlyAfterSuccessfulRead(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd not installed")
	}
	var compressed bytes.Buffer
	cmd := exec.Command("zstd", "-q", "-c")
	cmd.Stdin = bytes.NewBufferString("hello zstd")
	cmd.Stdout = &compressed
	if err := cmd.Run(); err != nil {
		t.Fatalf("zstd compress error: %v", err)
	}
	rc, err := NewReader("zstd", &compressed)
	if err != nil {
		t.Fatalf("NewReader error: %v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(got) != "hello zstd" {
		t.Fatalf("got %q", got)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
}
