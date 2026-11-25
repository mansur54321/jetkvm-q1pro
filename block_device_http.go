package kvm

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// A CD read must match the requested offset even if the origin changes its
// range support or the underlying file after the initial capacity probe.
type remoteHTTPImage struct {
	url       string
	size      int64
	validator string
}

var remoteBlockHTTP = &http.Client{Timeout: 10 * time.Second}

func rangeSize(header string, first, last int64) (int64, error) {
	fields := strings.Fields(header)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bytes") {
		return 0, fmt.Errorf("invalid HTTP Content-Range %q", header)
	}
	span, total, ok := strings.Cut(fields[1], "/")
	left, right, hasBounds := strings.Cut(span, "-")
	start, e1 := strconv.ParseUint(left, 10, 63)
	end, e2 := strconv.ParseUint(right, 10, 63)
	size, err := strconv.ParseUint(total, 10, 63)
	if !ok || !hasBounds || e1 != nil || e2 != nil || err != nil || size <= uint64(last) || start != uint64(first) || end != uint64(last) {
		return 0, fmt.Errorf("unexpected HTTP Content-Range %q for bytes %d-%d", header, first, last)
	}
	return int64(size), nil
}

func (r remoteHTTPImage) ReadAt(dst []byte, off int64) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	if off < 0 || off >= r.size || int64(len(dst)) > r.size-off {
		return 0, io.EOF
	}
	last := off + int64(len(dst)) - 1
	req, err := http.NewRequest(http.MethodGet, r.url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, last))
	req.Header.Set("Accept-Encoding", "identity")
	if r.validator != "" {
		req.Header.Set("If-Range", r.validator)
	}
	resp, err := remoteBlockHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("HTTP range read returned %s", resp.Status)
	}
	size, err := rangeSize(resp.Header.Get("Content-Range"), off, last)
	if err != nil {
		return 0, err
	}
	if size != r.size || (resp.ContentLength >= 0 && resp.ContentLength != int64(len(dst))) {
		return 0, fmt.Errorf("remote image size or HTTP range length changed")
	}
	if r.validator != "" {
		header := "Last-Modified"
		if strings.HasPrefix(r.validator, "\"") {
			header = "ETag"
		}
		if actual := resp.Header.Get(header); actual != "" && actual != r.validator {
			return 0, fmt.Errorf("remote image validator changed")
		}
	}
	if enc := resp.Header.Get("Content-Encoding"); enc != "" && !strings.EqualFold(enc, "identity") {
		return 0, fmt.Errorf("unexpected HTTP range encoding %q", enc)
	}
	return io.ReadFull(resp.Body, dst)
}
