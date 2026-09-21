package pdf

// trailing.go: 混合 table/stream 检测与尾部未被 xref 引用数据检测。

import "bytes"

func (s *scanner) detectMixedAndTrailing() {
	kinds := map[string]bool{}
	for _, sec := range s.sections {
		kinds[sec.kind] = true
	}
	s.rep.MixedXRef = kinds["table"] && kinds["stream"]
	if s.rep.MixedXRef {
		s.diag(DiagInfo, "mixed-xref", s.sections[0].offset,
			"previous 链上同时存在 xref table 与 xref stream（混合增量更新）", "")
	}

	if len(s.sections) == 0 {
		return
	}
	latestOrder := 0
	var blockEnd int
	for _, sec := range s.sections {
		if sec.scanOrder == latestOrder {
			off := sec.offset
			rel := bytes.Index(s.data[off:], []byte("%%EOF"))
			if rel >= 0 {
				blockEnd = off + rel + len("%%EOF")
			} else {
				blockEnd = off
			}
		}
	}
	// 跳过尾随空白
	end := len(s.data)
	tail := end
	for tail > blockEnd {
		b := s.data[tail-1]
		if isWhitespace(b) {
			tail--
			continue
		}
		break
	}
	if tail > blockEnd {
		s.rep.TrailingFrom = blockEnd
		s.rep.TrailingLen = end - blockEnd
		s.diag(DiagWarn, "trailing-data", blockEnd,
			"最新 %%EOF 之后追加了未被任何 xref 引用的数据",
			snippet(s.data, blockEnd, 32)+" ... (共 "+itoa(end-blockEnd)+" 字节)")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
