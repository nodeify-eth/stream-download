package extract

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Limits struct {
	MaxBytes int64
	MaxFiles int64
}

func ExtractTar(r io.Reader, dest string, limits Limits) error {
	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	tr := tar.NewReader(r)
	var bytesWritten int64
	var files int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeTarget(root, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			files++
			if limits.MaxFiles > 0 && files > limits.MaxFiles {
				return fmt.Errorf("max extracted files exceeded")
			}
			if limits.MaxBytes > 0 && bytesWritten+h.Size > limits.MaxBytes {
				return fmt.Errorf("max extracted bytes exceeded")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode)&0777)
			if err != nil {
				return err
			}
			n, copyErr := io.Copy(f, tr)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if n != h.Size {
				return io.ErrUnexpectedEOF
			}
			bytesWritten += n
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("links are not allowed: %q -> %q", h.Name, h.Linkname)
		default:
			return fmt.Errorf("unsupported tar entry type %d for %q", h.Typeflag, h.Name)
		}
	}
}

func safeTarget(root, name string) (string, error) {
	clean := filepath.Clean(name)
	if filepath.IsAbs(name) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	target := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("archive path escapes destination: %q", name)
	}
	return target, nil
}
