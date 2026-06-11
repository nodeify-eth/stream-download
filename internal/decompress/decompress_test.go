package decompress

import (
	"bytes"
	"compress/gzip"
	"io"
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
