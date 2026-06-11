package decompress

import (
	"compress/gzip"
	"fmt"
	"io"
)

type readCloser struct {
	io.Reader
	close func() error
}

func (r readCloser) Close() error {
	if r.close != nil {
		return r.close()
	}
	return nil
}

func NewReader(kind string, r io.Reader) (io.ReadCloser, error) {
	switch kind {
	case "", "none":
		return readCloser{Reader: r}, nil
	case "gzip", "gz":
		return gzip.NewReader(r)
	default:
		return nil, fmt.Errorf("compression %q is not supported by this reader", kind)
	}
}

func CommandFor(kind string) ([]string, error) {
	switch kind {
	case "", "none", "gzip", "gz":
		return nil, nil
	case "zstd", "zst":
		return []string{"zstd", "-dc"}, nil
	case "lz4":
		return []string{"lz4", "-dc"}, nil
	case "xz":
		return []string{"xz", "-dc"}, nil
	default:
		return nil, fmt.Errorf("unsupported compression %q", kind)
	}
}
