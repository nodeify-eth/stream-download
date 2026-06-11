package source

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPResolveAndReadRange(t *testing.T) {
	body := []byte("0123456789")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", "Thu, 11 Jun 2026 00:00:00 GMT")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			return
		}
		if r.Header.Get("Range") != "bytes=2-5" {
			t.Fatalf("Range = %q, want bytes=2-5", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Range", "bytes 2-5/10")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[2:6])
	}))
	defer srv.Close()

	src := NewHTTP(srv.URL, nil)
	id, err := src.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if id.Size != int64(len(body)) || id.ETag != `"abc"` {
		t.Fatalf("identity = %+v", id)
	}
	rc, gotID, err := src.ReadRange(context.Background(), Range{Start: 2, End: 5}, id)
	if err != nil {
		t.Fatalf("ReadRange error: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "2345" {
		t.Fatalf("body = %q", got)
	}
	if gotID.Size != id.Size {
		t.Fatalf("range identity = %+v", gotID)
	}
}

func TestHTTPRejectsLyingContentRange(t *testing.T) {
	body := []byte("0123456789")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			return
		}
		w.Header().Set("Content-Range", "bytes 2-4/10")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[2:6])
	}))
	defer srv.Close()

	src := NewHTTP(srv.URL, nil)
	id, err := src.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	rc, _, err := src.ReadRange(context.Background(), Range{Start: 2, End: 5}, id)
	if err == nil {
		rc.Close()
		t.Fatalf("ReadRange succeeded, want Content-Range error")
	}
}
