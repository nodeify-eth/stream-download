package spool

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

func TestEmitOrderedOutOfOrderChunks(t *testing.T) {
	chunks := []Chunk{
		{Index: 1, Start: 3, End: 5, Data: []byte("def")},
		{Index: 0, Start: 0, End: 2, Data: []byte("abc")},
	}
	var out bytes.Buffer
	err := EmitOrdered(context.Background(), chunks, &out)
	if err != nil {
		t.Fatalf("EmitOrdered error: %v", err)
	}
	if out.String() != "abcdef" {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRejectGap(t *testing.T) {
	chunks := []Chunk{{Index: 1, Start: 3, End: 5, Data: []byte("def")}}
	var out bytes.Buffer
	err := EmitOrdered(context.Background(), chunks, &out)
	if err == nil {
		t.Fatalf("EmitOrdered succeeded, want gap error")
	}
}

func TestRejectDuplicateOffset(t *testing.T) {
	chunks := []Chunk{
		{Index: 0, Start: 0, End: 2, Data: []byte("abc")},
		{Index: 1, Start: 0, End: 2, Data: []byte("abc")},
	}
	var out bytes.Buffer
	err := EmitOrdered(context.Background(), chunks, &out)
	if err == nil {
		t.Fatalf("EmitOrdered succeeded, want duplicate offset error")
	}
}

func TestDownstreamFailureCancels(t *testing.T) {
	chunks := []Chunk{{Index: 0, Start: 0, End: 2, Data: []byte("abc")}}
	err := EmitOrdered(context.Background(), chunks, failingWriter{})
	if err == nil {
		t.Fatalf("EmitOrdered succeeded, want write error")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

var _ io.Writer = failingWriter{}
