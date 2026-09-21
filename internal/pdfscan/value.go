package pdfscan

// ValueKind 标识解析值的类型。
const (
	VNull      = "null"
	VBool      = "bool"
	VInt       = "int"
	VReal      = "real"
	VName      = "name"
	VString    = "string"
	VHexString = "hexstring"
	VArray     = "array"
	VDict      = "dict"
	VRef       = "ref"
	VStream    = "stream" // 字典带流，字典本身存在 Dict 中
)

// KV 是字典里的一个键值对（保留出现顺序）。
type KV struct {
	Key   string `json:"key"`
	Value *Value `json:"value"`
}

// Value 是解析后的 PDF 值。字符串同时保留可打印预览与原始字节。
type Value struct {
	Kind        string  `json:"kind"`
	Bool        bool    `json:"bool,omitempty"`
	Int         int64   `json:"int,omitempty"`
	Real        float64 `json:"real,omitempty"`
	Name        string  `json:"name,omitempty"`
	Text        string  `json:"text,omitempty"`        // 可打印预览（可能有替换字符）
	Hex         bool    `json:"hex,omitempty"`         // 是否为十六进制字符串
	Truncated   bool    `json:"truncated,omitempty"`
	RawLen      int     `json:"rawLen,omitempty"`
	RawBase64   string  `json:"rawBase64,omitempty"`   // 仅小字符串（<= rawInlineLimit）
	Array       []*Value `json:"array,omitempty"`
	Dict        []KV    `json:"dict,omitempty"`
	RefNum      int     `json:"refNum,omitempty"`
	RefGen      int     `json:"refGen,omitempty"`
	Stream      *StreamInfo `json:"stream,omitempty"`
}

// DictGet 返回字典中键对应的值（不存在返回 nil）。
func (v *Value) DictGet(name string) *Value {
	if v == nil || v.Kind != VDict && v.Kind != VStream {
		return nil
	}
	for i := range v.Dict {
		if v.Dict[i].Key == name {
			return v.Dict[i].Value
		}
	}
	return nil
}

// AsInt 尝试把整型/实型值取成 int64。
func (v *Value) AsInt() (int64, bool) {
	if v == nil {
		return 0, false
	}
	if v.Kind == VInt {
		return v.Int, true
	}
	if v.Kind == VReal {
		return int64(v.Real), true
	}
	return 0, false
}

// AsName 返回 /Name 的字符串形式。
func (v *Value) AsName() (string, bool) {
	if v != nil && v.Kind == VName {
		return v.Name, true
	}
	return "", false
}
