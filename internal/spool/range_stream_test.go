package spool

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"

	"github.com/nodeify-eth/stream-download/internal/source"
)

func TestStreamRangesRetriesShortRange(t *testing.T) {
	data := []byte("abcdefghijklmnopqrstuvwxyz")
	src := &retryRangeSource{data: data}
	id := source.Identity{Kind: "test", Size: int64(len(data)), ETag: `"retry"`}

	rc, err := StreamRanges(context.Background(), src, id, 8, 2, t.TempDir(), 2)
	if err != nil {
		t.Fatalf("StreamRanges error: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("streamed data = %q, want %q", got, data)
	}
	if src.attemptsFor(0) != 2 {
		t.Fatalf("range 0 attempts = %d, want 2", src.attemptsFor(0))
	}
}

type retryRangeSource struct {
	mu       sync.Mutex
	data     []byte
	attempts map[int64]int
}

func (s *retryRangeSource) Resolve(context.Context) (source.Identity, error) {
	return source.Identity{Kind: "test", Size: int64(len(s.data)), ETag: `"retry"`}, nil
}

func (s *retryRangeSource) ReadRange(_ context.Context, r source.Range, id source.Identity) (io.ReadCloser, source.Identity, error) {
	s.mu.Lock()
	if s.attempts == nil {
		s.attempts = map[int64]int{}
	}
	s.attempts[r.Start]++
	attempt := s.attempts[r.Start]
	s.mu.Unlock()

	chunk := s.data[r.Start : r.End+1]
	if r.Start == 0 && attempt == 1 {
		chunk = chunk[:len(chunk)-1]
	}
	return io.NopCloser(bytes.NewReader(chunk)), id, nil
}

func (s *retryRangeSource) attemptsFor(start int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts[start]
}
