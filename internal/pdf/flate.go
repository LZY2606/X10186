package pdf

// flate.go: 仅在长度、filter、边界核验通过后展开压缩流；
// 超出限额时安全停止并返回 LimitError，绝不截断后假装成功。

import (
	"bytes"
	"compress/zlib"
	"errors"
	"io"
)

// LimitError 表示解压触顶。
type LimitError struct {
	Limit int
}

func (e *LimitError) Error() string { return "decompressed stream exceeded limit bytes" }

// ErrLimit 限额哨兵。
var ErrLimit = &LimitError{}

// filterName 归一化过滤器名称（支持简写）。
func filterName(v *Value) (string, bool) {
	if v == nil {
		return "", true
	}
	switch v.Kind {
	case "null":
		return "", true
	case "name":
		return v.Text, true
	case "array":
		if len(v.Items) == 0 {
			return "", true
		}
		// 页脉探针只处理纯 FlateDecode；链中含其它过滤器则不展开。
		names := make([]string, 0, len(v.Items))
		for _, it := range v.Items {
			if it.Kind != "name" {
				return "", false
			}
			names = append(names, it.Text)
		}
		return joinFilters(names), true
	}
	return "", false
}

func joinFilters(ns []string) string {
	out := ""
	for i, n := range ns {
		if i > 0 {
			out += ","
		}
		out += n
	}
	return out
}

// supportedFilter 判断 filter 链是否可由本工具安全展开。
func supportedFilter(f string) bool {
	switch f {
	case "", "FlateDecode", "Fl":
		return true
	}
	return false
}

// limitReader 安全解压：超过 maxOut 即返回 ErrLimit。
type limitReader struct {
	r      io.ReadCloser
	out    int
	maxOut int
}

func (l *limitReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.out += n
	if l.out > l.maxOut {
		return n, ErrLimit
	}
	return n, err
}
func (l *limitReader) Close() error { return l.r.Close() }

// inflate 在给定上限内 zlib 解压。
func inflate(src []byte, maxOut int) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	lr := &limitReader{r: zr, maxOut: maxOut}
	var buf bytes.Buffer
	step := make([]byte, 32*1024)
	for {
		n, rerr := lr.Read(step)
		if n > 0 {
			buf.Write(step[:n])
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			zr.Close()
			if errors.Is(rerr, ErrLimit) {
				return nil, ErrLimit
			}
			return nil, rerr
		}
		if buf.Len() > maxOut {
			zr.Close()
			return nil, ErrLimit
		}
	}
	if err := zr.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
