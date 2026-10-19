package models

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// maxHeader is how large a safetensors header may be: it is a JSON map of
// tensor names, and even a model with a million tensors keeps it in
// single-digit megabytes.
const maxHeader = 64 << 20

// ReadSafetensorsHeader returns the tensor names of a .safetensors file:
// the file opens with the header's length as a little-endian 64-bit count
// and the header's JSON. It reads that header and nothing else, so a file
// of any size is classified without loading a weight.
func ReadSafetensorsHeader(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lenb [8]byte
	if _, err := f.Read(lenb[:]); err != nil {
		return nil, fmt.Errorf("a .safetensors header is at least 8 bytes")
	}
	n := binary.LittleEndian.Uint64(lenb[:])
	if n == 0 || n > maxHeader {
		return nil, fmt.Errorf("not a .safetensors header: %d bytes is not a header size", n)
	}
	header := make([]byte, n)
	if _, err := f.Read(header); err != nil {
		return nil, fmt.Errorf("the header says %d bytes", n)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(header, &m); err != nil {
		return nil, fmt.Errorf("not a .safetensors header: %w", err)
	}
	var out []string
	for name := range m {
		if name == "__metadata__" {
			continue
		}
		out = append(out, name)
	}
	return out, nil
}

// ClassifyTensors reads a shelf off tensor names. Only names that state what
// the weights are get a verdict; a header that states nothing is unknown.
func ClassifyTensors(names []string) Shelf {
	for _, n := range names {
		l := strings.ToLower(n)
		switch {
		case strings.Contains(l, "lora_"),
			strings.Contains(l, "diffusion_model."),
			strings.HasPrefix(l, "first_stage_model."),
			strings.HasPrefix(l, "vae."),
			strings.HasPrefix(l, "text_model."),
			strings.HasPrefix(l, "conditioner."):
			return ShelfImage
		case strings.HasPrefix(l, "language_model."),
			strings.HasPrefix(l, "model.language_model."),
			strings.HasPrefix(l, "model.layers."):
			return ShelfChat
		}
	}
	return ""
}
