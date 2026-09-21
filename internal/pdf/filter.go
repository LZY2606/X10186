package pdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
)

// ErrLimit is returned when a decoded stream would exceed the resource limit.
var ErrLimit = fmt.Errorf("decode limit exceeded")

// Limits bounds scanner resource usage so damaged files stop safely.
type Limits struct {
	MaxDecodeBytes int64 // maximum decompressed size of a single stream
	MaxObjects     int   // maximum indirect objects indexed
	MaxSections    int   // maximum xref sections followed via /Prev
}

// DefaultLimits are conservative defaults for interactive review.
func DefaultLimits() Limits {
	return Limits{MaxDecodeBytes: 32 << 20, MaxObjects: 200000, MaxSections: 1000}
}

func (l Limits) withDefaults() Limits {
	if l.MaxDecodeBytes <= 0 {
		l.MaxDecodeBytes = 32 << 20
	}
	if l.MaxObjects <= 0 {
		l.MaxObjects = 200000
	}
	if l.MaxSections <= 0 {
		l.MaxSections = 1000
	}
	return l
}

// filterNames normalizes the /Filter entry to a list of filter names.
func filterNames(d Dict) ([]Name, error) {
	v, ok := d.Get("Filter")
	if !ok {
		return nil, nil
	}
	switch f := v.(type) {
	case Name:
		return []Name{f}, nil
	case Array:
		out := make([]Name, 0, len(f))
		for _, e := range f {
			n, ok := e.(Name)
			if !ok {
				return nil, fmt.Errorf("non-name in /Filter array")
			}
			out = append(out, n)
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported /Filter type %T", v)
}

// decodeParms returns the per-filter decode parameter dictionaries.
func decodeParms(d Dict, n int) []Dict {
	out := make([]Dict, n)
	v, ok := d.Get("DecodeParms")
	if !ok {
		return out
	}
	switch p := v.(type) {
	case Dict:
		if n > 0 {
			out[0] = p
		}
	case Array:
		for i := 0; i < len(p) && i < n; i++ {
			if pd, ok := p[i].(Dict); ok {
				out[i] = pd
			}
		}
	}
	return out
}

// DecodeStream applies the stream's filters. It only runs after the caller
// has verified length, filter names and stream boundaries.
func DecodeStream(d Dict, raw []byte, lim Limits) ([]byte, error) {
	lim = lim.withDefaults()
	names, err := filterNames(d)
	if err != nil {
		return nil, err
	}
	parms := decodeParms(d, len(names))
	data := raw
	for i, name := range names {
		switch name {
		case "FlateDecode", "Fl":
			data, err = flateDecode(data, lim.MaxDecodeBytes)
		case "ASCIIHexDecode", "AHx":
			data, err = asciiHexDecode(data, lim.MaxDecodeBytes)
		case "ASCII85Decode", "A85":
			data, err = ascii85Decode(data, lim.MaxDecodeBytes)
		case "RunLengthDecode", "RL":
			data, err = runLengthDecode(data, lim.MaxDecodeBytes)
		default:
			return nil, fmt.Errorf("unsupported filter %q", name)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if i < len(parms) && parms[i] != nil {
			data, err = applyPredictor(data, parms[i], lim.MaxDecodeBytes)
			if err != nil {
				return nil, err
			}
		}
	}
	if int64(len(data)) > lim.MaxDecodeBytes {
		return nil, ErrLimit
	}
	return data, nil
}

func flateDecode(data []byte, limit int64) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return readLimited(r, limit)
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if n > limit {
		return nil, ErrLimit
	}
	return buf.Bytes(), nil
}

func asciiHexDecode(data []byte, limit int64) ([]byte, error) {
	out := make([]byte, 0, len(data)/2)
	var hi int = -1
	for _, b := range data {
		if b == '>' {
			break
		}
		v := hexVal(b)
		if v < 0 {
			continue
		}
		if hi < 0 {
			hi = v
		} else {
			out = append(out, byte(hi<<4|v))
			hi = -1
			if int64(len(out)) > limit {
				return nil, ErrLimit
			}
		}
	}
	if hi >= 0 {
		out = append(out, byte(hi<<4))
	}
	return out, nil
}

func hexVal(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	}
	return -1
}

func ascii85Decode(data []byte, limit int64) ([]byte, error) {
	out := make([]byte, 0, len(data)*4/5)
	var group [5]byte
	n := 0
	flush := func(count int) {
		if count == 0 {
			return
		}
		for i := count; i < 5; i++ {
			group[i] = 'u'
		}
		var v uint32
		for i := 0; i < 5; i++ {
			v = v*85 + uint32(group[i]-'!')
		}
		b := []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
		out = append(out, b[:count-1]...)
	}
	for _, b := range data {
		if b == '~' {
			break
		}
		if b == 'z' && n == 0 {
			out = append(out, 0, 0, 0, 0)
			continue
		}
		if b < '!' || b > 'u' {
			continue
		}
		group[n] = b
		n++
		if n == 5 {
			flush(5)
			n = 0
			if int64(len(out)) > limit {
				return nil, ErrLimit
			}
		}
	}
	if n > 0 {
		flush(n)
	}
	if int64(len(out)) > limit {
		return nil, ErrLimit
	}
	return out, nil
}

func runLengthDecode(data []byte, limit int64) ([]byte, error) {
	var out []byte
	for i := 0; i < len(data); {
		n := int(data[i])
		i++
		switch {
		case n == 128:
			return out, nil
		case n < 128:
			count := n + 1
			if i+count > len(data) {
				return nil, fmt.Errorf("runlength literal overrun")
			}
			out = append(out, data[i:i+count]...)
			i += count
		default:
			count := 257 - n
			if i >= len(data) {
				return nil, fmt.Errorf("runlength repeat overrun")
			}
			for j := 0; j < count; j++ {
				out = append(out, data[i])
			}
			i++
		}
		if int64(len(out)) > limit {
			return nil, ErrLimit
		}
	}
	return out, nil
}

// applyPredictor reverses TIFF (2) and PNG (10-15) predictors.
func applyPredictor(data []byte, parms Dict, limit int64) ([]byte, error) {
	pred, ok := parms.Int("Predictor")
	if !ok || pred <= 1 {
		return data, nil
	}
	cols, _ := parms.Int("Columns")
	if cols <= 0 {
		cols = 1
	}
	colors, _ := parms.Int("Colors")
	if colors <= 0 {
		colors = 1
	}
	bpc, _ := parms.Int("BitsPerComponent")
	if bpc <= 0 {
		bpc = 8
	}
	if pred == 2 {
		if bpc != 8 {
			return nil, fmt.Errorf("TIFF predictor with %d bpc unsupported", bpc)
		}
		row := int(cols * colors)
		if row <= 0 || len(data)%row != 0 {
			return nil, fmt.Errorf("TIFF predictor: bad row size")
		}
		out := make([]byte, len(data))
		copy(out, data)
		for r := 0; r < len(out); r += row {
			for c := int(colors); c < row; c++ {
				out[r+c] += out[r+c-int(colors)]
			}
		}
		return out, nil
	}
	if pred >= 10 && pred <= 15 {
		stride := int((cols*colors*bpc + 7) / 8)
		bpp := int((colors*bpc + 7) / 8)
		if bpp < 1 {
			bpp = 1
		}
		rowLen := stride + 1
		if stride <= 0 || len(data)%rowLen != 0 {
			return nil, fmt.Errorf("PNG predictor: bad row size")
		}
		out := make([]byte, 0, len(data))
		prev := make([]byte, stride)
		for r := 0; r < len(data); r += rowLen {
			ft := data[r]
			row := append([]byte(nil), data[r+1:r+rowLen]...)
			switch ft {
			case 0:
			case 1: // Sub
				for i := bpp; i < stride; i++ {
					row[i] += row[i-bpp]
				}
			case 2: // Up
				for i := 0; i < stride; i++ {
					row[i] += prev[i]
				}
			case 3: // Average
				for i := 0; i < stride; i++ {
					a := 0
					if i >= bpp {
						a = int(row[i-bpp])
					}
					row[i] += byte((a + int(prev[i])) / 2)
				}
			case 4: // Paeth
				for i := 0; i < stride; i++ {
					a, b, c := 0, int(prev[i]), 0
					if i >= bpp {
						a = int(row[i-bpp])
						c = int(prev[i-bpp])
					}
					row[i] += byte(paeth(a, b, c))
				}
			default:
				return nil, fmt.Errorf("PNG predictor: bad filter type %d", ft)
			}
			out = append(out, row...)
			copy(prev, row)
			if int64(len(out)) > limit {
				return nil, ErrLimit
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported predictor %d", pred)
}

func paeth(a, b, c int) int {
	p := a + b - c
	pa, pb, pc := abs(p-a), abs(p-b), abs(p-c)
	if pa <= pb && pa <= pc {
		return a
	}
	if pb <= pc {
		return b
	}
	return c
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
