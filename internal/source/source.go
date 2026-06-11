package source

import (
	"context"
	"io"
)

type Identity struct {
	Kind         string
	URL          string
	Size         int64
	ETag         string
	LastModified string
	VersionID    string
	Weak         bool
}

type Range struct {
	Start int64
	End   int64
}

func (r Range) Size() int64 {
	return r.End - r.Start + 1
}

type Reader interface {
	Resolve(context.Context) (Identity, error)
	ReadRange(context.Context, Range, Identity) (io.ReadCloser, Identity, error)
}
