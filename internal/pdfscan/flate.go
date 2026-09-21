package pdfscan

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
)

// ErrLimitExceeded 表示解压超出资源限额。
var ErrLimitExceeded = errors.New("解压超出资源限额")

// decodeStreamData 按 /Filter 链解码流数据（仅支持 FlateDecode/Fl），
// 强制 MaxExpandedBytes 与 MaxExpandRatio 限额。
func decodeStreamData(raw []byte, dict *Value, limits Limits) ([]byte, error) {
	filters := streamFilters(dict)
	if len(filters) == 0 {
		return raw, nil
	}
	data := raw
	for _, f := range filters {
		switch f {
		case "FlateDecode", "Fl":
			out, err := inflateLimited(data, limits)
			if err != nil {
				return nil, err
			}
			data = out
		case "ASCIIHexDecode", "AHx":
			data = asciiHexDecode(data)
		case "ASCII85Decode", "A85":
			out, err := ascii85Decode(data, limits.MaxExpandedBytes)
			if err != nil {
				return nil, err
			}
			data = out
		default:
			return nil, fmt.Errorf("不支持的过滤器 %s", f)
		}
	}
	return data, nil
}

// streamFilters 提取规范化过滤器名列表。
func streamFilters(dict *Value) []string {
	fv := dict.DictGet("Filter")
	if fv == nil {
		return nil
	}
	var out []string
	if fv.Kind == VName {
		out = append(out, fv.Name)
	} else if fv.Kind == VArray {
		for _, it := range fv.Array {
			if it.Kind == VName {
				out = append(out, it.Name)
			}
		}
	}
	return out
}

// inflateLimited 解压 zlib 流，限制输出字节数与膨胀比。
func inflateLimited(raw []byte, limits Limits) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("zlib 初始化失败: %w", err)
	}
	defer zr.Close()
	limit := limits.MaxExpandedBytes
	if limit <= 0 {
		limit = 64 << 20
	}
	lr := &io.LimitedReader{R: zr, N: limit + 1}
	out, err := io.ReadAll(lr)
	if err != nil {
		return nil, fmt.Errorf("zlib 解压失败: %w", err)
	}
	if int64(len(out)) > limit {
		return nil, fmt.Errorf("%w: 超过最大展开字节 %d", ErrLimitExceeded, limit)
	}
	if limits.MaxExpandRatio > 0 && len(raw) > 0 {
		ratio := len(out) / len(raw)
		if ratio > limits.MaxExpandRatio {
			return nil, fmt.Errorf("%w: 膨胀比 %d 超过上限 %d", ErrLimitExceeded, ratio, limits.MaxExpandRatio)
		}
	}
	return out, nil
}

func asciiHexDecode(data []byte) []byte {
	var digits []byte
	for _, c := range data {
		if c == '>' {
			break
		}
		if isWhitespace(c) {
			continue
		}
		digits = append(digits, c)
	}
	if len(digits)%2 == 1 {
		digits = append(digits, '0')
	}
	out := make([]byte, len(digits)/2)
	for i := 0; i < len(digits); i += 2 {
		out[i/2] = byte(hexVal(digits[i])<<4 | hexVal(digits[i+1]))
	}
	return out
}

func ascii85Decode(data []byte, maxOut int64) ([]byte, error) {
	var out []byte
	var group []byte
	for i := 0; i < len(data); i++ {
		c := data[i]
		if isWhitespace(c) {
			continue
		}
		if c == '~' && i+1 < len(data) && data[i+1] == '>' {
			break
		}
		if c == 'z' && len(group) == 0 {
			out = append(out, 0, 0, 0, 0)
			continue
		}
		if c < '!' || c > 'u' {
			return nil, errors.New("ASCII85 非法字符")
		}
		group = append(group, c)
		if len(group) == 5 {
			var v uint32
			for _, g := range group {
				v = v*85 + uint32(g-'!')
			}
			out = append(out, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
			group = group[:0]
			if maxOut > 0 && int64(len(out)) > maxOut {
				return nil, ErrLimitExceeded
			}
		}
	}
	if len(group) > 1 {
		n := len(group)
		for len(group) < 5 {
			group = append(group, 'u')
		}
		var v uint32
		for _, g := range group {
			v = v*85 + uint32(g-'!')
		}
		full := []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
		out = append(out, full[:n-1]...)
	}
	return out, nil
}
