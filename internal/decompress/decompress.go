package decompress

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os/exec"
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
		args, err := CommandFor(kind)
		if err != nil {
			return nil, err
		}
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdin = r
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return commandReadCloser{Reader: stdout, wait: cmd.Wait, kill: func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}, stderr: &stderr}, nil
	}
}

type commandReadCloser struct {
	io.Reader
	wait   func() error
	kill   func()
	stderr *bytes.Buffer
}

func (c commandReadCloser) Close() error {
	if c.kill != nil {
		c.kill()
	}
	if c.wait == nil {
		return nil
	}
	if err := c.wait(); err != nil {
		if c.stderr != nil && c.stderr.Len() > 0 {
			return fmt.Errorf("%w: %s", err, c.stderr.String())
		}
		return err
	}
	return nil
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
