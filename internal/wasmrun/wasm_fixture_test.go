package wasmrun

import (
	"bytes"
	"encoding/binary"
)

// buildEchoModule emits a small WASM module that implements the digest ABI:
//
//   - imports relayfile.host_log and relayfile.host_change_events;
//   - exports a 1-page memory, a bump-allocator (`alloc`), and a `digest`
//     function whose i64 return packs (ptr<<32)|len pointing at a baked
//     DigestSection JSON literal.
//
// The module is hand-encoded so the test suite does not depend on an external
// WAT→WASM toolchain (TinyGo, wasm-tools, …). It is the smallest fixture that
// exercises the full runtime contract: memory exports, the alloc bridge for
// host_change_events results, and a meaningful section return value.
func buildEchoModule(sectionJSON string, filterJSON string) []byte {
	// Memory layout — keep in sync with the digest code below.
	const (
		offsetLevel   uint32 = 0  // "info"
		offsetMessage uint32 = 4  // "hello"
		offsetSection uint32 = 16 // sectionJSON
		offsetFilter  uint32 = 64 // filterJSON (must remain below 256, the
		//                          initial brk value used by `alloc`).
	)
	level := []byte("info")
	message := []byte("hello")
	sectionBytes := []byte(sectionJSON)
	filterBytes := []byte(filterJSON)

	if offsetFilter+uint32(len(filterBytes)) > 256 {
		panic("wasmrun test fixture: filter overlaps allocator nursery; bump bump-allocator base")
	}
	if offsetSection+uint32(len(sectionBytes)) > offsetFilter {
		panic("wasmrun test fixture: section overlaps filter region")
	}

	var w bytes.Buffer
	w.Write([]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00})

	writeSection(&w, 0x01, typeSection())
	writeSection(&w, 0x02, importSection())
	writeSection(&w, 0x03, functionSection())
	writeSection(&w, 0x05, memorySection())
	writeSection(&w, 0x06, globalSection())
	writeSection(&w, 0x07, exportSection())
	writeSection(&w, 0x0a, codeSection(offsetLevel, uint32(len(level)),
		offsetMessage, uint32(len(message)),
		offsetFilter, uint32(len(filterBytes)),
		offsetSection, uint32(len(sectionBytes))))
	writeSection(&w, 0x0b, dataSection(
		offsetLevel, level,
		offsetMessage, message,
		offsetSection, sectionBytes,
		offsetFilter, filterBytes,
	))

	return w.Bytes()
}

func writeSection(out *bytes.Buffer, id byte, payload []byte) {
	out.WriteByte(id)
	writeULEB(out, uint64(len(payload)))
	out.Write(payload)
}

func writeULEB(out *bytes.Buffer, v uint64) {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			out.WriteByte(b | 0x80)
			continue
		}
		out.WriteByte(b)
		return
	}
}

func writeSLEB(out *bytes.Buffer, v int64) {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if (v == 0 && b&0x40 == 0) || (v == -1 && b&0x40 != 0) {
			out.WriteByte(b)
			return
		}
		out.WriteByte(b | 0x80)
	}
}

func writeName(out *bytes.Buffer, name string) {
	writeULEB(out, uint64(len(name)))
	out.WriteString(name)
}

func typeSection() []byte {
	var b bytes.Buffer
	writeULEB(&b, 4)
	// t0: () -> i64
	b.Write([]byte{0x60, 0x00, 0x01, 0x7e})
	// t1: (i32 i32 i32 i32) -> ()
	b.Write([]byte{0x60, 0x04, 0x7f, 0x7f, 0x7f, 0x7f, 0x00})
	// t2: (i32 i32) -> i64
	b.Write([]byte{0x60, 0x02, 0x7f, 0x7f, 0x01, 0x7e})
	// t3: (i32) -> i32
	b.Write([]byte{0x60, 0x01, 0x7f, 0x01, 0x7f})
	return b.Bytes()
}

func importSection() []byte {
	var b bytes.Buffer
	writeULEB(&b, 2)
	writeName(&b, "relayfile")
	writeName(&b, "host_log")
	b.WriteByte(0x00)
	writeULEB(&b, 1) // type idx
	writeName(&b, "relayfile")
	writeName(&b, "host_change_events")
	b.WriteByte(0x00)
	writeULEB(&b, 2) // type idx
	return b.Bytes()
}

func functionSection() []byte {
	var b bytes.Buffer
	writeULEB(&b, 2)
	writeULEB(&b, 3) // alloc -> type 3
	writeULEB(&b, 0) // digest -> type 0
	return b.Bytes()
}

func memorySection() []byte {
	var b bytes.Buffer
	writeULEB(&b, 1)
	b.WriteByte(0x00) // limits: min only
	writeULEB(&b, 1)  // 1 page
	return b.Bytes()
}

func globalSection() []byte {
	var b bytes.Buffer
	writeULEB(&b, 1)
	b.WriteByte(0x7f) // i32
	b.WriteByte(0x01) // mut
	b.WriteByte(0x41) // i32.const
	writeSLEB(&b, 256)
	b.WriteByte(0x0b) // end
	return b.Bytes()
}

func exportSection() []byte {
	var b bytes.Buffer
	writeULEB(&b, 3)
	writeName(&b, "memory")
	b.WriteByte(0x02)
	writeULEB(&b, 0)
	writeName(&b, "alloc")
	b.WriteByte(0x00)
	writeULEB(&b, 2)
	writeName(&b, "digest")
	b.WriteByte(0x00)
	writeULEB(&b, 3)
	return b.Bytes()
}

func codeSection(levelPtr, levelLen, msgPtr, msgLen, filterPtr, filterLen, sectionPtr, sectionLen uint32) []byte {
	allocBody := allocFunctionBody()
	digestBody := digestFunctionBody(levelPtr, levelLen, msgPtr, msgLen, filterPtr, filterLen, sectionPtr, sectionLen)

	var b bytes.Buffer
	writeULEB(&b, 2)

	writeULEB(&b, uint64(len(allocBody)))
	b.Write(allocBody)
	writeULEB(&b, uint64(len(digestBody)))
	b.Write(digestBody)

	return b.Bytes()
}

func allocFunctionBody() []byte {
	var b bytes.Buffer
	writeULEB(&b, 0) // no locals beyond the param
	b.WriteByte(0x23) // global.get
	writeULEB(&b, 0)
	b.WriteByte(0x23)
	writeULEB(&b, 0)
	b.WriteByte(0x20) // local.get
	writeULEB(&b, 0)
	b.WriteByte(0x6a) // i32.add
	b.WriteByte(0x24) // global.set
	writeULEB(&b, 0)
	b.WriteByte(0x0b) // end
	return b.Bytes()
}

func digestFunctionBody(levelPtr, levelLen, msgPtr, msgLen, filterPtr, filterLen, sectionPtr, sectionLen uint32) []byte {
	var b bytes.Buffer
	writeULEB(&b, 0)

	// host_log(levelPtr, levelLen, msgPtr, msgLen)
	pushI32(&b, levelPtr)
	pushI32(&b, levelLen)
	pushI32(&b, msgPtr)
	pushI32(&b, msgLen)
	b.WriteByte(0x10) // call
	writeULEB(&b, 0)

	// drop = host_change_events(filterPtr, filterLen)
	pushI32(&b, filterPtr)
	pushI32(&b, filterLen)
	b.WriteByte(0x10)
	writeULEB(&b, 1)
	b.WriteByte(0x1a) // drop

	// return packed (sectionPtr<<32)|sectionLen
	pushI64(&b, packPointer(sectionPtr, sectionLen))
	b.WriteByte(0x0b) // end
	return b.Bytes()
}

func pushI32(b *bytes.Buffer, v uint32) {
	b.WriteByte(0x41)
	writeSLEB(b, int64(int32(v)))
}

func pushI64(b *bytes.Buffer, v uint64) {
	b.WriteByte(0x42)
	writeSLEB(b, int64(v))
}

func dataSection(segments ...interface{}) []byte {
	if len(segments)%2 != 0 {
		panic("dataSection requires (offset, bytes) pairs")
	}
	var b bytes.Buffer
	writeULEB(&b, uint64(len(segments)/2))
	for i := 0; i < len(segments); i += 2 {
		offset := segments[i].(uint32)
		payload := segments[i+1].([]byte)
		b.WriteByte(0x00) // active, memory 0
		b.WriteByte(0x41) // i32.const
		writeSLEB(&b, int64(offset))
		b.WriteByte(0x0b)
		writeULEB(&b, uint64(len(payload)))
		b.Write(payload)
	}
	return b.Bytes()
}

// _ keeps binary referenced even when no caller needs little-endian helpers.
var _ = binary.LittleEndian
