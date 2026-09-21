package pdf

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Ref is an indirect object reference (num gen R).
type Ref struct {
	Num int `json:"num"`
	Gen int `json:"gen"`
}

func (r Ref) String() string { return fmt.Sprintf("%d %d R", r.Num, r.Gen) }

// Name is a PDF name object (without the leading slash).
type Name string

// PdfString is a literal or hexadecimal string object.
type PdfString string

// Dict is a PDF dictionary.
type Dict map[string]Value

// Array is a PDF array.
type Array []Value

// Value is any parsed PDF object value.
type Value any

// Stream pairs a dictionary with its (still encoded) stream bytes.
type Stream struct {
	Dict Dict   `json:"-"`
	Data []byte `json:"-"`
}

// Get returns a dictionary value by key.
func (d Dict) Get(key string) (Value, bool) {
	if d == nil {
		return nil, false
	}
	v, ok := d[key]
	return v, ok
}

// Int returns an integer dictionary entry.
func (d Dict) Int(key string) (int64, bool) {
	v, ok := d.Get(key)
	if !ok {
		return 0, false
	}
	n, ok := v.(int64)
	return n, ok
}

// Name returns a name dictionary entry.
func (d Dict) Name(key string) (Name, bool) {
	v, ok := d.Get(key)
	if !ok {
		return "", false
	}
	n, ok := v.(Name)
	return n, ok
}

// Render converts a parsed value into a compact human-readable form.
func Render(v Value) string {
	var sb strings.Builder
	renderValue(&sb, v, 0)
	return sb.String()
}

func renderValue(sb *strings.Builder, v Value, depth int) {
	if depth > 12 {
		sb.WriteString("...")
		return
	}
	switch t := v.(type) {
	case nil:
		sb.WriteString("null")
	case bool:
		sb.WriteString(strconv.FormatBool(t))
	case int64:
		sb.WriteString(strconv.FormatInt(t, 10))
	case float64:
		sb.WriteString(strconv.FormatFloat(t, 'g', -1, 64))
	case Name:
		sb.WriteString("/" + string(t))
	case PdfString:
		sb.WriteString(strconv.Quote(string(t)))
	case Ref:
		sb.WriteString(t.String())
	case Array:
		sb.WriteString("[")
		for i, e := range t {
			if i > 0 {
				sb.WriteString(" ")
			}
			renderValue(sb, e, depth+1)
		}
		sb.WriteString("]")
	case Dict:
		sb.WriteString("<<")
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sb.WriteString(" /")
			sb.WriteString(k)
			sb.WriteString(" ")
			renderValue(sb, t[k], depth+1)
		}
		sb.WriteString(" >>")
	case *Stream:
		renderValue(sb, t.Dict, depth+1)
		fmt.Fprintf(sb, " stream(%d bytes)", len(t.Data))
	default:
		fmt.Fprintf(sb, "%v", t)
	}
}

// CollectRefs walks a parsed value and gathers every indirect reference.
func CollectRefs(v Value, out *[]Ref) {
	switch t := v.(type) {
	case Ref:
		*out = append(*out, t)
	case Array:
		for _, e := range t {
			CollectRefs(e, out)
		}
	case Dict:
		for _, e := range t {
			CollectRefs(e, out)
		}
	case *Stream:
		CollectRefs(t.Dict, out)
	}
}
