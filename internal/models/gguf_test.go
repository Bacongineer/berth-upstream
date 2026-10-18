package models

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// The synthetic file is the real layout, not a reader's own idea of it: a GGUF
// parser tested against a writer that shares its mistakes proves nothing.

// putStr, putU32 and putU64 write the three widths every GGUF field is made of.
func putStr(t *testing.T, b *bytes.Buffer, s string) {
	t.Helper()
	if err := binary.Write(b, binary.LittleEndian, uint64(len(s))); err != nil {
		t.Fatal(err)
	}
	b.WriteString(s)
}

func putU32(t *testing.T, b *bytes.Buffer, v uint32) {
	t.Helper()
	if err := binary.Write(b, binary.LittleEndian, v); err != nil {
		t.Fatal(err)
	}
}

func putU64(t *testing.T, b *bytes.Buffer, v uint64) {
	t.Helper()
	if err := binary.Write(b, binary.LittleEndian, v); err != nil {
		t.Fatal(err)
	}
}

// kv is one metadata pair, written according to the Go type of its value.
type kv struct {
	key string
	val any
}

// tensor is one tensor info: enough of a weight matrix to count it.
type tensor struct {
	name string
	dims []uint64
	typ  uint32
}

// buildGGUF writes a header and its tensor infos. No tensor data follows: the
// reader must never need it, and a file without weights is the cheapest thing
// to test against.
func buildGGUF(t *testing.T, version uint32, kvs []kv, ts []tensor) []byte {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("GGUF")
	putU32(t, &b, version)
	putU64(t, &b, uint64(len(ts)))
	putU64(t, &b, uint64(len(kvs)))
	for _, kv := range kvs {
		putStr(t, &b, kv.key)
		switch v := kv.val.(type) {
		case string:
			putU32(t, &b, typeString)
			putStr(t, &b, v)
		case bool:
			putU32(t, &b, typeBool)
			if v {
				b.WriteByte(1)
			} else {
				b.WriteByte(0)
			}
		case uint32:
			putU32(t, &b, typeUint32)
			putU32(t, &b, v)
		case int32:
			putU32(t, &b, typeInt32)
			if err := binary.Write(&b, binary.LittleEndian, v); err != nil {
				t.Fatal(err)
			}
		case float32:
			putU32(t, &b, typeFloat32)
			if err := binary.Write(&b, binary.LittleEndian, v); err != nil {
				t.Fatal(err)
			}
		case []int32:
			putU32(t, &b, typeArray)
			putU32(t, &b, typeInt32)
			putU64(t, &b, uint64(len(v)))
			for _, x := range v {
				if err := binary.Write(&b, binary.LittleEndian, x); err != nil {
					t.Fatal(err)
				}
			}
		case []string:
			putU32(t, &b, typeArray)
			putU32(t, &b, typeString)
			putU64(t, &b, uint64(len(v)))
			for _, x := range v {
				putStr(t, &b, x)
			}
		default:
			t.Fatalf("kv %q: no GGUF encoding for %T", kv.key, kv.val)
		}
	}
	for _, tn := range ts {
		putStr(t, &b, tn.name)
		putU32(t, &b, uint32(len(tn.dims)))
		for _, d := range tn.dims {
			putU64(t, &b, d)
		}
		putU32(t, &b, tn.typ)
		putU64(t, &b, 0) // offset: the data is not here
	}
	return b.Bytes()
}

// sampleGGUF is the file the helpers are asserted against: a llama with a
// window, layers, heads, a quant, a vocabulary too long to keep and a chat
// template, in three tensors.
func sampleGGUF(t *testing.T) []byte {
	t.Helper()
	vocab := make([]int32, 100)
	for i := range vocab {
		vocab[i] = int32(i)
	}
	return buildGGUF(t, 3, []kv{
		{"general.architecture", "llama"},
		{"general.name", "Synthetic 8B"},
		{"general.file_type", uint32(15)},
		{"general.quantization_version", uint32(2)},
		{"llama.context_length", uint32(8192)},
		{"llama.block_count", uint32(2)},
		{"llama.embedding_length", uint32(4096)},
		{"llama.attention.head_count_kv", []int32{4, 8}},
		{"tokenizer.ggml.tokens", vocab},
		{"tokenizer.ggml.bos_token_ids", []string{"<s>", "«sys»"}},
		{"tokenizer.ggml.add_bos_token", true},
		{"tokenizer.chat_template", "{{ message }}\nsecond line"},
	}, []tensor{
		{"token_embd.weight", []uint64{4096, 32000}, 1},
		{"blk.0.attn_q.weight", []uint64{4096, 4096}, 1},
		{"output.weight", []uint64{4096, 32000}, 8},
	})
}

// writeGGUF puts bytes at path, ready for a reader that only takes a name.
func writeGGUF(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// readGGUF reads bytes back once, through a file, the way the library does.
func readGGUF(t *testing.T, data []byte) (*GGUF, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "m.gguf")
	writeGGUF(t, p, data)
	return ReadGGUF(p)
}

func TestReadGGUFArchitecture(t *testing.T) {
	g, err := readGGUF(t, sampleGGUF(t))
	if err != nil {
		t.Fatalf("ReadGGUF: %v", err)
	}
	if g.Version != 3 {
		t.Errorf("Version = %d, want 3", g.Version)
	}
	if g.Tensors != 3 {
		t.Errorf("Tensors = %d, want 3", g.Tensors)
	}
	if got, want := g.Arch(), "llama"; got != want {
		t.Errorf("Arch = %q, want %q", got, want)
	}
	if got, want := g.Name(), "Synthetic 8B"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	if got := g.ContextLength(); got != 8192 {
		t.Errorf("ContextLength = %d, want 8192", got)
	}
	if got := g.Layers(); got != 2 {
		t.Errorf("Layers = %d, want 2", got)
	}
	if got := g.EmbeddingLength(); got != 4096 {
		t.Errorf("EmbeddingLength = %d, want 4096", got)
	}
	// The array of KV heads reads as its largest member, not its first.
	if got := g.HeadsKV(); got != 8 {
		t.Errorf("HeadsKV = %d, want 8 (the array is 4, 8)", got)
	}
	if got, want := g.FileType(), "Q4_K_M"; got != want {
		t.Errorf("FileType = %q, want %q (file_type 15)", got, want)
	}
	if got := g.SplitCount(); got != 0 {
		t.Errorf("SplitCount = %d, want 0 for a file that is not split", got)
	}
	if got, want := g.ChatTemplate(), "{{ message }}\nsecond line"; got != want {
		t.Errorf("ChatTemplate = %q, want %q", got, want)
	}
	// 4096*32000 twice and 4096*4096 once.
	want := uint64(4096*32000*2 + 4096*4096)
	if g.Params != want {
		t.Errorf("Params = %d, want %d", g.Params, want)
	}
	if g.TypeCounts[1] != 2 || g.TypeCounts[8] != 1 {
		t.Errorf("TypeCounts = %v, want two F16 and one Q8_0", g.TypeCounts)
	}
}

func TestReadGGUFValues(t *testing.T) {
	g, err := readGGUF(t, sampleGGUF(t))
	if err != nil {
		t.Fatalf("ReadGGUF: %v", err)
	}
	if got := g.Meta["general.architecture"]; got != "llama" {
		t.Errorf("general.architecture = %#v, want the string", got)
	}
	if got := g.Meta["llama.context_length"]; got != uint32(8192) {
		t.Errorf("llama.context_length = %#v, want the u32", got)
	}
	if got := g.Meta["tokenizer.ggml.add_bos_token"]; got != true {
		t.Errorf("add_bos_token = %#v, want true", got)
	}
	// A hundred tokens is past the keep-everything line: the length stays, the
	// members do not.
	big, ok := g.Meta["tokenizer.ggml.tokens"].(Array)
	if !ok {
		t.Fatalf("tokenizer.ggml.tokens = %#v, want an Array", g.Meta["tokenizer.ggml.tokens"])
	}
	if big.Len != 100 || big.Vals != nil {
		t.Errorf("tokens = len %d, %d members: want 100 members kept as a length only", big.Len, len(big.Vals))
	}
	if big.ElemType != typeInt32 {
		t.Errorf("tokens element type = %d, want i32", big.ElemType)
	}
	// A short array is kept whole, strings and all.
	small, ok := g.Meta["tokenizer.ggml.bos_token_ids"].(Array)
	if !ok {
		t.Fatalf("bos_token_ids = %#v, want an Array", g.Meta["tokenizer.ggml.bos_token_ids"])
	}
	if len(small.Vals) != 2 || small.Vals[1] != "«sys»" {
		t.Errorf("bos_token_ids = %#v, want both members", small.Vals)
	}
}

func TestReadGGUFFallsBackToTensorType(t *testing.T) {
	// No general.file_type: the quant a file never stamped is still the type
	// most of its tensors are in.
	data := buildGGUF(t, 3, []kv{{"general.architecture", "qwen2"}}, []tensor{
		{"a", []uint64{8, 8}, 1}, {"b", []uint64{8, 8}, 1}, {"c", []uint64{8, 8}, 8},
	})
	g, err := readGGUF(t, data)
	if err != nil {
		t.Fatalf("ReadGGUF: %v", err)
	}
	if got, want := g.FileType(), "F16"; got != want {
		t.Errorf("FileType = %q, want %q (two of three tensors are F16)", got, want)
	}
	if got := g.ContextLength(); got != 0 {
		t.Errorf("ContextLength = %d, want 0 when the file states none", got)
	}
}

func TestReadGGUFSplitCount(t *testing.T) {
	data := buildGGUF(t, 3, []kv{
		{"general.architecture", "llama"},
		{"split.count", uint32(3)},
	}, []tensor{{"a", []uint64{4, 4}, 1}})
	g, err := readGGUF(t, data)
	if err != nil {
		t.Fatalf("ReadGGUF: %v", err)
	}
	if got := g.SplitCount(); got != 3 {
		t.Errorf("SplitCount = %d, want 3", got)
	}
}

func TestReadGGUFRejects(t *testing.T) {
	full := sampleGGUF(t)
	cases := []struct {
		name  string
		data  []byte
		where string
	}{
		{"not gguf", []byte("hello there, not a gguf at all"), "not a GGUF"},
		{"too short", []byte("GGU"), "EOF"},
		{"version 1", []byte("GGUF\x01\x00\x00\x00"), "version 1"},
		{"version 99", []byte("GGUFc\x00\x00\x00"), "version 99"},
		// A header that stops in the middle of a value must error, not panic.
		{"truncated", full[:len(full)/2], ""},
		{"cut before the tensors", full[:40], ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readGGUF(t, tc.data)
			if err == nil {
				t.Fatal("ReadGGUF accepted a file it should refuse")
			}
			if tc.where != "" && !bytes.Contains([]byte(err.Error()), []byte(tc.where)) {
				t.Errorf("error = %q, want it to mention %q", err, tc.where)
			}
		})
	}
}

func TestReadGGUFRefusesHugeString(t *testing.T) {
	// One metadata pair, whose string length is 64 GiB. A u64 like that is a
	// valid number and a wrong file: the reader must say so before it allocates.
	var b bytes.Buffer
	b.WriteString("GGUF")
	putU32(t, &b, 3)
	putU64(t, &b, 0)
	putU64(t, &b, 1)
	putStr(t, &b, "general.architecture")
	putU32(t, &b, typeString)
	putU64(t, &b, 1<<36)
	if _, err := readGGUF(t, b.Bytes()); err == nil {
		t.Fatal("a 64 GiB string was accepted")
	} else if !bytes.Contains([]byte(err.Error()), []byte("string length")) {
		t.Errorf("error = %v, want it to name the string length", err)
	}
}

func TestReadGGUFRefusesHugeArray(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("GGUF")
	putU32(t, &b, 3)
	putU64(t, &b, 0)
	putU64(t, &b, 1)
	putStr(t, &b, "tokenizer.ggml.tokens")
	putU32(t, &b, typeArray)
	putU32(t, &b, typeString)
	putU64(t, &b, 1<<40)
	if _, err := readGGUF(t, b.Bytes()); err == nil {
		t.Fatal("an array of 2^40 members was accepted")
	}
}

func TestReadGGUFMissingFile(t *testing.T) {
	if _, err := ReadGGUF(filepath.Join(t.TempDir(), "nope.gguf")); err == nil {
		t.Fatal("a file that is not there was read")
	}
}
