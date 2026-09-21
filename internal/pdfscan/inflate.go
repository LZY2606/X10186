package pdfscan

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
)

// ErrInflateLimit is returned when decompressed data exceeds the resource limit.
var ErrInflateLimit = errors.New("decompression size limit exceeded")

func inflateFlate(data []byte, maxOut int64) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, maxOut+1))
	if err != nil {
		return nil, err
	}
	if n > maxOut {
		return nil, ErrInflateLimit
	}
	return buf.Bytes(), nil
}

// applyFilter decodes stream data. Only FlateDecode is supported; anything
// else is reported as unsupported rather than silently passed through.
func applyFilter(data []byte, filter string, maxOut int64) ([]byte, error) {
	switch filter {
	case "", "none":
		if int64(len(data)) > maxOut {
			return nil, ErrInflateLimit
		}
		return data, nil
	case "FlateDecode", "Fl":
		return inflateFlate(data, maxOut)
	default:
		return nil, fmt.Errorf("unsupported filter %q", filter)
	}
}
