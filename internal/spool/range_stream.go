package spool

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/nodeify-eth/stream-download/internal/source"
)

type rangeResult struct {
	index int
	path  string
	err   error
}

func StreamRanges(ctx context.Context, src source.Reader, id source.Identity, rangeSize int64, concurrency int, scratchDir string) (io.ReadCloser, error) {
	if id.Size <= 0 {
		return nil, fmt.Errorf("source size must be known for range streaming")
	}
	if rangeSize <= 0 {
		return nil, fmt.Errorf("range size must be positive")
	}
	if concurrency <= 0 {
		return nil, fmt.Errorf("concurrency must be positive")
	}
	if err := os.MkdirAll(scratchDir, 0700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	go streamRanges(ctx, cancel, src, id, rangeSize, concurrency, scratchDir, pw)
	return pr, nil
}

func streamRanges(ctx context.Context, cancel context.CancelFunc, src source.Reader, id source.Identity, rangeSize int64, concurrency int, scratchDir string, pw *io.PipeWriter) {
	defer cancel()
	total := int((id.Size + rangeSize - 1) / rangeSize)
	results := make(chan rangeResult, concurrency)
	pending := map[int]rangeResult{}
	nextStart := 0
	active := 0

	start := func(index int) {
		active++
		go func() {
			results <- downloadRange(ctx, src, id, rangeSize, scratchDir, index)
		}()
	}
	fill := func() {
		for nextStart < total && active+len(pending) < concurrency {
			start(nextStart)
			nextStart++
		}
	}
	cleanup := func(err error) {
		for _, res := range pending {
			if res.path != "" {
				_ = os.Remove(res.path)
			}
		}
		_ = pw.CloseWithError(err)
	}

	fill()
	for emit := 0; emit < total; {
		if res, ok := pending[emit]; ok {
			if res.err != nil {
				cleanup(res.err)
				return
			}
			if err := copyAndDelete(res.path, pw); err != nil {
				cleanup(err)
				return
			}
			delete(pending, emit)
			emit++
			fill()
			continue
		}
		if active == 0 {
			cleanup(fmt.Errorf("range stream stalled before chunk %d", emit))
			return
		}
		select {
		case <-ctx.Done():
			cleanup(ctx.Err())
			return
		case res := <-results:
			active--
			pending[res.index] = res
		}
	}
	_ = pw.Close()
}

func downloadRange(ctx context.Context, src source.Reader, id source.Identity, rangeSize int64, scratchDir string, index int) rangeResult {
	start := int64(index) * rangeSize
	end := start + rangeSize - 1
	if end >= id.Size {
		end = id.Size - 1
	}
	r := source.Range{Start: start, End: end}
	rc, _, err := src.ReadRange(ctx, r, id)
	if err != nil {
		return rangeResult{index: index, err: err}
	}
	defer rc.Close()

	tmp := filepath.Join(scratchDir, fmt.Sprintf("range-%06d.tmp", index))
	final := filepath.Join(scratchDir, fmt.Sprintf("range-%06d", index))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return rangeResult{index: index, err: err}
	}
	n, copyErr := io.Copy(f, rc)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return rangeResult{index: index, err: copyErr}
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return rangeResult{index: index, err: closeErr}
	}
	if n != r.Size() {
		_ = os.Remove(tmp)
		return rangeResult{index: index, err: fmt.Errorf("range %d returned %d bytes, want %d", index, n, r.Size())}
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return rangeResult{index: index, err: err}
	}
	return rangeResult{index: index, path: final}
}

func copyAndDelete(path string, out io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, f)
	closeErr := f.Close()
	removeErr := os.Remove(path)
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}
