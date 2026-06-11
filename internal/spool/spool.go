package spool

import (
	"context"
	"fmt"
	"io"
	"sort"
)

type Chunk struct {
	Index int
	Start int64
	End   int64
	Data  []byte
}

func EmitOrdered(ctx context.Context, chunks []Chunk, out io.Writer) error {
	ordered := append([]Chunk(nil), chunks...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Index < ordered[j].Index })
	var next int64
	for _, ch := range ordered {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ch.Start != next {
			return fmt.Errorf("chunk gap or duplicate: got start %d, want %d", ch.Start, next)
		}
		if int64(len(ch.Data)) != ch.End-ch.Start+1 {
			return fmt.Errorf("chunk %d size mismatch", ch.Index)
		}
		n, err := out.Write(ch.Data)
		if err != nil {
			return err
		}
		if n != len(ch.Data) {
			return io.ErrShortWrite
		}
		next = ch.End + 1
	}
	return nil
}
