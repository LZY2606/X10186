package forensic

import (
	"bytes"
	"compress/zlib"
	"fmt"
)

// builder assembles byte-exact PDF fixtures with controllable damage.
type builder struct {
	buf    bytes.Buffer
	off    map[int]int
	gens   map[int]int
	chunks map[int]int // insert position -> length of damage, handled manually
}

func newBuilder() *builder {
	b := &builder{off: map[int]int{}, gens: map[int]int{}, chunks: map[int]int{}}
	b.buf.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	return b
}

func (b *builder) put(num, gen int, body string) {
	b.off[num] = b.buf.Len()
	b.gens[num] = gen
	fmt.Fprintf(&b.buf, "%d %d obj\n%s\nendobj\n", num, gen, body)
}

// putRaw writes an object header plus raw content and records the offset.
func (b *builder) putRaw(num, gen int, content string) {
	b.off[num] = b.buf.Len()
	b.gens[num] = gen
	fmt.Fprintf(&b.buf, "%d %d obj\n", num, gen)
	b.buf.WriteString(content)
}

func (b *builder) write(s string) { b.buf.WriteString(s) }
func (b *builder) insertAt(pos int, s string) {
	raw := b.buf.Bytes()
	var out bytes.Buffer
	out.Write(raw[:pos])
	out.WriteString(s)
	out.Write(raw[pos:])
	b.buf = out
}

func (b *builder) bytes() []byte { return b.buf.Bytes() }

// classicXRef writes a table xref + trailer and returns its offset.
// free: num->(next,gen); alive: nums considered n entries.
func (b *builder) classicXRef(size int, free map[[2]int][2]int, alive []int, prev int, hasPrev bool, root int) int {
	off := b.buf.Len()
	b.buf.WriteString("xref\n")
	fmt.Fprintf(&b.buf, "0 %d\n", size)
	// row 0 free head
	head := [2]int{0, 65535}
	if f, ok := free[[2]int{0, 0}]; ok {
		head = f
	}
	fmt.Fprintf(&b.buf, "%010d %05d f \n", head[0], head[1])
	for n := 1; n < size; n++ {
		var f [2]int
		isFree := false
		for k, v := range free {
			if k[0] == n {
				f, isFree = v, true
				break
			}
		}
		if isFree {
			fmt.Fprintf(&b.buf, "%010d %05d f \n", f[0], f[1])
			continue
		}
		o := b.off[n]
		fmt.Fprintf(&b.buf, "%010d %05d n \n", o, b.gens[n])
	}
	b.buf.WriteString("trailer\n<< /Size ")
	fmt.Fprintf(&b.buf, "%d /Root %d 0 R", size, root)
	if hasPrev {
		fmt.Fprintf(&b.buf, " /Prev %d", prev)
	}
	b.buf.WriteString(" >>\nstartxref\n")
	fmt.Fprintf(&b.buf, "%d\n%%%%EOF\n", off)
	return off
}

func zlibBytes(in []byte) []byte {
	var out bytes.Buffer
	w := zlib.NewWriter(&out)
	w.Write(in)
	w.Close()
	return out.Bytes()
}

// streamXRef builds an object-stream xref with W=[1 2 1] style entries.
type sxEntry struct {
	typ byte
	f2  int64
	gen int
}

func (b *builder) streamXRef(objNum, size int, ents []sxEntry, index []int, prev int, hasPrev bool) (offset int, decoded []byte) {
	var raw bytes.Buffer
	for _, e := range ents {
		raw.WriteByte(e.typ)
		raw.WriteString(fmt.Sprintf("%010d", e.f2))
		raw.WriteString(fmt.Sprintf("%05d", e.gen))
	}
	decoded = raw.Bytes()
	comp := zlibBytes(decoded)
	off := b.buf.Len()
	fmt.Fprintf(&b.buf, "%d 0 obj\n<< /Type /XRef /Size %d /W [1 10 5] /Length %d /Filter /FlateDecode", objNum, size, len(comp))
	if len(index) > 0 {
		raw2 := "/Index ["
		for _, x := range index {
			raw2 += fmt.Sprintf(" %d", x)
		}
		raw2 += " ] "
		b.buf.WriteString(raw2)
	}
	if hasPrev {
		fmt.Fprintf(&b.buf, " /Prev %d", prev)
	}
	b.buf.WriteString(" >>\nstream\n")
	b.buf.Write(comp)
	b.buf.WriteString("\nendstream\nendobj\nstartxref\n")
	fmt.Fprintf(&b.buf, "%d\n%%%%EOF\n", off)
	return off, decoded
}

// objStream writes an ObjStm containing nums with simple int/dict values.
// It returns the container object number; member header pairs are N/offset.
func (b *builder) objStream(container int, members map[int]string) int {
	var body bytes.Buffer
	offs := map[int]int{}
	// first pass offsets
	keys := make([]int, 0, len(members))
	for k := range members {
		keys = append(keys, k)
	}
	// sort keys
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	var header bytes.Buffer
	cursor := 0
	for _, k := range keys {
		fmt.Fprintf(&header, "%d %d ", k, cursor)
		offs[k] = cursor
		cursor += len(members[k]) + 1
	}
	body.Write(header.Bytes())
	for i, k := range keys {
		body.WriteString(members[k])
		if i < len(keys)-1 {
			body.WriteByte(' ')
		}
	}
	comp := zlibBytes(body.Bytes())
	b.off[container] = b.buf.Len()
	fmt.Fprintf(&b.buf, "%d 0 obj\n<< /Type /ObjStm /N %d /First %d /Length %d /Filter /FlateDecode >>\nstream\n",
		container, len(keys), header.Len(), len(comp))
	b.buf.Write(comp)
	b.buf.WriteString("\nendstream\nendobj\n")
	return container
}
