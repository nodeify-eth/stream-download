package source

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type HTTP struct {
	url    string
	client *http.Client
}

func NewHTTP(url string, client *http.Client) *HTTP {
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTP{url: url, client: client}
}

func (h *HTTP) Resolve(ctx context.Context) (Identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, h.url, nil)
	if err != nil {
		return Identity{}, err
	}
	res, err := h.client.Do(req)
	if err != nil {
		return Identity{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Identity{}, fmt.Errorf("HEAD returned HTTP %d", res.StatusCode)
	}
	size, _ := strconv.ParseInt(res.Header.Get("Content-Length"), 10, 64)
	etag := res.Header.Get("ETag")
	return Identity{
		Kind:         "http",
		URL:          h.url,
		Size:         size,
		ETag:         etag,
		LastModified: res.Header.Get("Last-Modified"),
		Weak:         strings.HasPrefix(strings.ToLower(etag), "w/"),
	}, nil
}

func (h *HTTP) ReadRange(ctx context.Context, r Range, pinned Identity) (io.ReadCloser, Identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.url, nil)
	if err != nil {
		return nil, Identity{}, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", r.Start, r.End))
	if pinned.ETag != "" && !pinned.Weak {
		req.Header.Set("If-Match", pinned.ETag)
	}
	res, err := h.client.Do(req)
	if err != nil {
		return nil, Identity{}, err
	}
	if res.StatusCode != http.StatusPartialContent {
		res.Body.Close()
		return nil, Identity{}, fmt.Errorf("range returned HTTP %d, want 206", res.StatusCode)
	}
	if err := validateContentRange(res.Header.Get("Content-Range"), r, pinned.Size); err != nil {
		res.Body.Close()
		return nil, Identity{}, err
	}
	return res.Body, pinned, nil
}

func validateContentRange(header string, r Range, total int64) error {
	want := fmt.Sprintf("bytes %d-%d/%d", r.Start, r.End, total)
	if header != want {
		return fmt.Errorf("Content-Range = %q, want %q", header, want)
	}
	return nil
}
