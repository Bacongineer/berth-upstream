// Package models reads the model files on this machine. It reads headers only:
// a GGUF's metadata and tensor infos, a safetensors dir's config.json, an
// Ollama manifest and its model blob. Nothing here loads weights, hashes a
// file or talks to a registry, and a file that is not a model is a report,
// never a crash.
package models

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// The GGUF value types, as they appear in a header's u32 type field.
const (
	typeUint8 = iota
	typeInt8
	typeUint16
	typeInt16
	typeUint32
	typeInt32
	typeFloat32
	typeBool
	typeString
	typeArray
	typeUint64
	typeInt64
	typeFloat64
)

// Caps on what a header may ask this reader to allocate. A GGUF is untrusted
// input: a 64 TiB string length is a valid u64 and a wrong answer, and Berth
// is a daemon that must not be talked into an allocation by a stray file.
const (
	maxStringLen = 1 << 20 // 1 MiB: no metadata string is longer
	maxArrayLen  = 1 << 26 // a vocab is ~150k, so this is generous
	maxKVPairs   = 1 << 20 // every model so far is well under a thousand
	maxArrayKeep = 64      // longer arrays keep their length, not their members
	maxDims      = 4       // GGML_MAX_DIMS
	maxDim       = 1 << 40 // one dimension of one tensor
)

// Array is a metadata array. A long one keeps only its length: a tokenizer
// vocab is a hundred thousand strings and every reader of it is a converter,
// not a listing.
type Array struct {
	ElemType uint32 // the GGUF value type of the members
	Len      int    // the number of members
	Vals     []any  // nil when only the length was kept
}

// GGUF is what a header says about a file.
type GGUF struct {
	Version    uint32
	Tensors    uint64            // the tensor count the header states
	Meta       map[string]any    // every metadata pair, in the file's types
	Params     uint64            // parameters, summed over the tensor infos
	TypeCounts map[uint32]uint64 // tensors per storage type
	TypeParams map[uint32]uint64 // parameters per storage type
}

// ReadGGUF reads one file's header: the metadata pairs and the tensor infos,
// then stops. Tensor data is never read, so this is cheap on a 40 GiB file.
// A file that is not GGUF, is a version this reader does not speak, is
// truncated, or asks for an impossible allocation is an error.
func ReadGGUF(path string) (*GGUF, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)

	magic := make([]byte, 4)
	if _, err := io.ReadFull(r, magic); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if string(magic) != "GGUF" {
		return nil, fmt.Errorf("%s: not a GGUF file (magic %q)", path, magic)
	}

	g := &GGUF{Meta: map[string]any{}, TypeCounts: map[uint32]uint64{}, TypeParams: map[uint32]uint64{}}
	if err := binary.Read(r, binary.LittleEndian, &g.Version); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if g.Version != 2 && g.Version != 3 {
		return nil, fmt.Errorf("%s: GGUF version %d is not 2 or 3", path, g.Version)
	}
	// Version 1 has these two as u32s and a different array encoding; no file
	// in the wild uses it and reading it wrong is worse than refusing.
	if err := binary.Read(r, binary.LittleEndian, &g.Tensors); err != nil {
		return nil, fmt.Errorf("%s: tensor count: %w", path, err)
	}
	var kvCount uint64
	if err := binary.Read(r, binary.LittleEndian, &kvCount); err != nil {
		return nil, fmt.Errorf("%s: metadata count: %w", path, err)
	}
	if kvCount > maxKVPairs {
		return nil, fmt.Errorf("%s: %d metadata pairs is more than %d", path, kvCount, maxKVPairs)
	}
	for i := uint64(0); i < kvCount; i++ {
		key, err := readString(r)
		if err != nil {
			return nil, fmt.Errorf("%s: metadata %d: %w", path, i, err)
		}
		val, err := readValue(r, 0)
		if err != nil {
			return nil, fmt.Errorf("%s: metadata %q: %w", path, key, err)
		}
		g.Meta[key] = val
	}
	if err := g.readTensors(r, path); err != nil {
		return nil, err
	}
	return g, nil
}

// readTensors walks the tensor infos after the metadata: enough of the table to
// count parameters and the quant types in use, none of the weights.
func (g *GGUF) readTensors(r *bufio.Reader, path string) error {
	for i := uint64(0); i < g.Tensors; i++ {
		name, err := readString(r)
		if err != nil {
			return fmt.Errorf("%s: tensor %d: %w", path, i, err)
		}
		var nDims uint32
		if err := binary.Read(r, binary.LittleEndian, &nDims); err != nil {
			return fmt.Errorf("%s: tensor %q shape: %w", path, name, err)
		}
		if nDims > maxDims {
			return fmt.Errorf("%s: tensor %q has %d dimensions", path, name, nDims)
		}
		params := uint64(1)
		for d := uint32(0); d < nDims; d++ {
			var dim uint64
			if err := binary.Read(r, binary.LittleEndian, &dim); err != nil {
				return fmt.Errorf("%s: tensor %q shape: %w", path, name, err)
			}
			if dim > maxDim {
				return fmt.Errorf("%s: tensor %q has an implausible dimension %d", path, name, dim)
			}
			params *= dim
		}
		var typ uint32
		if err := binary.Read(r, binary.LittleEndian, &typ); err != nil {
			return fmt.Errorf("%s: tensor %q type: %w", path, name, err)
		}
		var offset uint64
		if err := binary.Read(r, binary.LittleEndian, &offset); err != nil {
			return fmt.Errorf("%s: tensor %q offset: %w", path, name, err)
		}
		g.Params += params
		g.TypeCounts[typ]++
		g.TypeParams[typ] += params
	}
	return nil
}

// readString reads a u64 length and that many bytes.
func readString(r *bufio.Reader) (string, error) {
	var n uint64
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return "", err
	}
	if n > maxStringLen {
		return "", fmt.Errorf("string length %d is more than %d", n, maxStringLen)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}

// readValue reads one metadata value of the given GGUF type. depth bounds the
// nesting an array can ask for.
func readValue(r *bufio.Reader, depth int) (any, error) {
	var typ uint32
	if err := binary.Read(r, binary.LittleEndian, &typ); err != nil {
		return nil, err
	}
	return readValueOf(r, typ, depth)
}

func readValueOf(r *bufio.Reader, typ uint32, depth int) (any, error) {
	switch typ {
	case typeUint8:
		var v uint8
		return v, binary.Read(r, binary.LittleEndian, &v)
	case typeInt8:
		var v int8
		return v, binary.Read(r, binary.LittleEndian, &v)
	case typeUint16:
		var v uint16
		return v, binary.Read(r, binary.LittleEndian, &v)
	case typeInt16:
		var v int16
		return v, binary.Read(r, binary.LittleEndian, &v)
	case typeUint32:
		var v uint32
		return v, binary.Read(r, binary.LittleEndian, &v)
	case typeInt32:
		var v int32
		return v, binary.Read(r, binary.LittleEndian, &v)
	case typeFloat32:
		var v float32
		return v, binary.Read(r, binary.LittleEndian, &v)
	case typeBool:
		var v uint8
		if err := binary.Read(r, binary.LittleEndian, &v); err != nil {
			return nil, err
		}
		return v != 0, nil
	case typeString:
		return readString(r)
	case typeUint64:
		var v uint64
		return v, binary.Read(r, binary.LittleEndian, &v)
	case typeInt64:
		var v int64
		return v, binary.Read(r, binary.LittleEndian, &v)
	case typeFloat64:
		var v float64
		return v, binary.Read(r, binary.LittleEndian, &v)
	case typeArray:
		return readArray(r, depth)
	}
	return nil, fmt.Errorf("unknown GGUF value type %d", typ)
}

func readArray(r *bufio.Reader, depth int) (any, error) {
	if depth > 4 {
		return nil, errors.New("metadata arrays nest too deeply")
	}
	var elemType uint32
	var n uint64
	if err := binary.Read(r, binary.LittleEndian, &elemType); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return nil, err
	}
	if n > maxArrayLen {
		return nil, fmt.Errorf("array of %d members is more than %d", n, maxArrayLen)
	}
	a := Array{ElemType: elemType, Len: int(n)}
	// Past the keeping line only the count survives: every member is still
	// parsed, because the stream has to be walked past it to reach the rest of
	// the header, but none of it is kept.
	keep := n <= maxArrayKeep
	if keep {
		a.Vals = make([]any, 0, n)
	}
	for i := uint64(0); i < n; i++ {
		v, err := readValueOf(r, elemType, depth+1)
		if err != nil {
			return nil, err
		}
		if keep {
			a.Vals = append(a.Vals, v)
		}
	}
	return a, nil
}

// Merge folds another part of the same split model in: the parameter count is
// the whole model's and the quant is what most of its tensors are, so neither
// can come from one part's header. The metadata stays the first part's, which
// is the one with the architecture, the vocab and the chat template.
func (g *GGUF) Merge(o *GGUF) {
	if o == nil {
		return
	}
	g.Params += o.Params
	for t, n := range o.TypeCounts {
		g.TypeCounts[t] += n
	}
	for t, n := range o.TypeParams {
		g.TypeParams[t] += n
	}
}

// metaUint reads a numeric metadata value whatever width the file used.
func (g *GGUF) metaUint(key string) (uint64, bool) {
	v, ok := g.Meta[key]
	if !ok {
		return 0, false
	}
	switch v := v.(type) {
	case uint8:
		return uint64(v), true
	case int8:
		return uint64(v), v >= 0
	case uint16:
		return uint64(v), true
	case int16:
		return uint64(v), v >= 0
	case uint32:
		return uint64(v), true
	case int32:
		return uint64(v), v >= 0
	case uint64:
		return v, true
	case int64:
		return uint64(v), v >= 0
	case float32:
		return uint64(v), v >= 0 && !math.IsNaN(float64(v))
	case float64:
		return uint64(v), v >= 0 && !math.IsNaN(v)
	}
	return 0, false
}

func (g *GGUF) metaString(key string) string {
	if s, ok := g.Meta[key].(string); ok {
		return s
	}
	return ""
}

// Arch is the architecture key the rest of the metadata hangs off.
func (g *GGUF) Arch() string { return g.metaString("general.architecture") }

// Name is the model's own display name.
func (g *GGUF) Name() string { return g.metaString("general.name") }

// ContextLength is the window the file was trained or converted for.
func (g *GGUF) ContextLength() uint64 {
	v, _ := g.metaUint(g.Arch() + ".context_length")
	return v
}

// Layers is the transformer block count.
func (g *GGUF) Layers() uint64 {
	v, _ := g.metaUint(g.Arch() + ".block_count")
	return v
}

// EmbeddingLength is the model's hidden size.
func (g *GGUF) EmbeddingLength() uint64 {
	v, _ := g.metaUint(g.Arch() + ".embedding_length")
	return v
}

// HeadsKV is the KV head count. Multi-head files store one value per layer, so
// an array reads as its largest member; 0 when the file says nothing.
func (g *GGUF) HeadsKV() uint64 {
	v, ok := g.Meta[g.Arch()+".attention.head_count_kv"]
	if !ok {
		return 0
	}
	if a, ok := v.(Array); ok {
		var best uint64
		for _, m := range a.Vals {
			if n, ok := toUint(m); ok && n > best {
				best = n
			}
		}
		return best
	}
	n, _ := toUint(v)
	return n
}

// FileType is the quant the converter stamped, named (15 is Q4_K_M), or the
// most common tensor type when the file states none or stamps a number this
// table does not know.
func (g *GGUF) FileType() string {
	if v, ok := g.metaUint("general.file_type"); ok {
		if name, known := fileTypes[uint32(v)]; known {
			return name
		}
		// A number this table predates is a quant the tensors can still name.
		// Inventing one from the number would be a guess dressed as metadata.
		if dom := g.dominantType(); dom != "" {
			return dom
		}
		return fmt.Sprintf("FTYPE%d", v)
	}
	return g.dominantType()
}

// The roles a file on disk can have. Only RoleModel runs on its own.
const (
	RoleModel     = "model"
	RoleMMProj    = "mmproj"
	RoleDraft     = "draft"
	RoleEmbedding = "embedding"
)

// roleWords say a file is not a model on its own: a projector for vision, a
// draft that speculates for a bigger model, both shipped as extra GGUFs next to
// the model they belong to.
//
// "mtp" is deliberately absent. In the current Qwen3.5-3.8 releases a file named
// ...-MTP-IQ4_XS is the whole model with its MTP head attached - 15 GiB of
// weights, run on its own - while the draft that goes with it is named FastMTP.
// A bare mtp in a file name is therefore not evidence of anything; the
// architecture is, and size beside its neighbour is (see refineDrafts).
var roleWords = []string{"mmproj", "mmprojector", "projector", "eagle", "dflash", "fastmtp", "draft"}

// draftArchs are the architectures that are a draft and nothing else.
var draftArchWords = []string{"mtp", "eagle", "dflash", "draft", "medusa"}

// Role is what this file is for: a model to run, or a companion that goes with
// one.
func (g *GGUF) Role(path string) string { return RoleOf(g.Arch(), path) }

// RoleOf is a file's role from its architecture and name, which is all a file
// whose header will not read still says about itself. clip is llama.cpp's
// architecture for a vision projector; a projector that spells itself out in
// the file name needs no architecture at all.
func RoleOf(arch, path string) string {
	arch = strings.ToLower(arch)
	if arch == "clip" || arch == "mmproj" {
		return RoleMMProj
	}
	name := strings.ToLower(filepath.Base(path))
	for _, w := range roleWords {
		if strings.Contains(arch, w) || strings.Contains(name, w) {
			return draftOrMMProj(w)
		}
	}
	for _, w := range draftArchWords {
		if strings.Contains(arch, w) {
			return RoleDraft
		}
	}
	if IsEmbeddingArch(arch) {
		return RoleEmbedding
	}
	return RoleModel
}

// draftOrMMProj splits the companion words: the projector words mean a vision
// adapter, the rest a speculative draft.
func draftOrMMProj(word string) string {
	switch word {
	case "mmproj", "mmprojector", "projector":
		return RoleMMProj
	}
	return RoleDraft
}

// RoleForArch is a file's role from its architecture alone, which is all a
// directory of safetensors states.
func RoleForArch(arch string) string { return RoleOf(arch, "") }

// IsEmbeddingArch reports whether an architecture produces vectors rather than
// tokens: the encoder models and the embedding finetunes of decoder ones.
func IsEmbeddingArch(arch string) bool {
	a := strings.ToLower(strings.TrimSpace(arch))
	switch a {
	case "bert", "nomic-bert", "jina-bert", "gte", "e5", "e5text", "arcticembed", "mamba-bert":
		return true
	}
	return strings.Contains(a, "embed") // gemma-embedding-*, bge-m3-style *-embedding
}

// dominantType is the storage type most of the file's parameters are in - the
// quant a file that never stamped one is still made of. Parameters rather than
// tensors, because a quantized file has a thousand one-dimensional norm vectors
// in F16 and its weights in Q4_K, and the F16 ones are not what it is. The
// tie-break is the type number, so the answer is the same twice in a row.
func (g *GGUF) dominantType() string {
	best, bestParams := -1, uint64(0)
	for t, n := range g.TypeParams {
		switch {
		case n > bestParams:
			best, bestParams = int(t), n
		case n == bestParams && best >= 0 && t < uint32(best):
			best = int(t)
		}
	}
	if bestParams == 0 {
		return ""
	}
	return TypeName(uint32(best))
}

// TypeName names a ggml storage type as a header numbers it.
func TypeName(t uint32) string {
	if name, known := ggmlTypes[t]; known {
		return name
	}
	return fmt.Sprintf("TYPE%d", t)
}

// SplitCount is how many files a split GGUF has, 0 when the file is not split.
func (g *GGUF) SplitCount() int {
	v, ok := g.metaUint("split.count")
	if !ok || v > math.MaxInt32 {
		return 0
	}
	return int(v)
}

// ChatTemplate is the converter's chat template, empty when the file has none.
func (g *GGUF) ChatTemplate() string { return g.metaString("tokenizer.chat_template") }

func toUint(v any) (uint64, bool) {
	switch v := v.(type) {
	case uint8:
		return uint64(v), true
	case int8:
		return uint64(v), v >= 0
	case uint16:
		return uint64(v), true
	case int16:
		return uint64(v), v >= 0
	case uint32:
		return uint64(v), true
	case int32:
		return uint64(v), v >= 0
	case uint64:
		return v, true
	case int64:
		return uint64(v), v >= 0
	}
	return 0, false
}

// ggmlTypes names a tensor's storage type, from ggml.h.
var ggmlTypes = map[uint32]string{
	0: "F32", 1: "F16", 2: "Q4_0", 3: "Q4_1", 6: "Q5_0", 7: "Q5_1", 8: "Q8_0",
	10: "Q2_K", 11: "Q3_K", 12: "Q4_K", 13: "Q5_K", 14: "Q6_K", 15: "Q8_K",
	16: "IQ2_XXS", 17: "IQ2_XS", 18: "IQ3_XXS", 19: "IQ1_S", 20: "IQ4_NL",
	21: "IQ3_S", 22: "IQ2_S", 23: "IQ4_XS", 24: "I8", 25: "I16", 26: "I32",
	27: "I64", 28: "F64", 29: "IQ1_M", 30: "BF16", 34: "TQ1_0", 35: "TQ2_0",
	39: "MXFP4",
}

// fileTypes is llama.cpp's enum llama_ftype as of b9600: what general.file_type
// states. The numbering is not the tensor type's, so 15 is a Q4_K_M file here
// and a Q8_K tensor there. The names are the enum's with MOSTLY_ dropped,
// which is how a quant is sold. 33-35 were the Q4_0 repack variants, removed,
// and no name is invented for them or for any other number this table leaves
// out: an unknown number says so and falls back to the dominant tensor type.
var fileTypes = map[uint32]string{
	0: "F32", 1: "F16", 2: "Q4_0", 3: "Q4_1", 4: "Q4_1_SOME_F16", 5: "Q4_2",
	6: "Q4_3", 7: "Q8_0", 8: "Q5_0", 9: "Q5_1", 10: "Q2_K", 11: "Q3_K_S",
	12: "Q3_K_M", 13: "Q3_K_L", 14: "Q4_K_S", 15: "Q4_K_M", 16: "Q5_K_S",
	17: "Q5_K_M", 18: "Q6_K", 19: "IQ2_XXS", 20: "IQ2_XS", 21: "Q2_K_S",
	22: "IQ3_XS", 23: "IQ3_XXS", 24: "IQ1_S", 25: "IQ4_NL", 26: "IQ3_S",
	27: "IQ3_M", 28: "IQ2_S", 29: "IQ2_M", 30: "IQ4_XS", 31: "IQ1_M",
	32: "BF16", 36: "TQ1_0", 37: "TQ2_0", 38: "MXFP4_MOE",
}
