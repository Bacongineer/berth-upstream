// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package models

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func ggufShelf(t *testing.T, arch, name string) Shelf {
	t.Helper()
	var data []byte
	if arch != "" {
		data = buildGGUF(t, 3, []kv{{"general.architecture", arch}}, []tensor{{"w", []uint64{4}, 1}})
	} else {
		data = buildGGUF(t, 3, nil, []tensor{{"w", []uint64{4}, 1}})
	}
	dir := t.TempDir()
	p := filepath.Join(dir, name+".gguf")
	writeGGUF(t, p, data)
	ms := Scan([]string{dir})
	if len(ms) != 1 {
		t.Fatalf("scan = %d models, want 1: %+v", len(ms), ms)
	}
	return ms[0].Shelf
}

func TestShelfOfGGUFArch(t *testing.T) {
	if s := ggufShelf(t, "llama", "qwen3-30b"); s != ShelfChat {
		t.Errorf("llama arch = %q, want chat", s)
	}
	if s := ggufShelf(t, "whisper", "whisper-large"); s != ShelfAudio {
		t.Errorf("whisper arch = %q, want audio", s)
	}
	if s := ggufShelf(t, "stable_diffusion", "sd15.gguf"); s != ShelfImage {
		t.Errorf("stable_diffusion arch = %q, want image", s)
	}
	// A header that reads no architecture falls through to the name.
	if s := ggufShelf(t, "", "kokoro-82m"); s != ShelfAudio {
		t.Errorf("nameless arch, kokoro name = %q, want audio", s)
	}
	// Neither header nor name states anything: unknown, not guessed.
	if s := ggufShelf(t, "", "mystery-weights"); s != "" {
		t.Errorf("undecided = %q, want no shelf", s)
	}
}

// safetensorsFile writes a header of the given tensors and a single weight
// byte, shaped exactly as the format lays it out.
func safetensorsFile(t *testing.T, path string, tensors map[string]any) {
	t.Helper()
	header, err := json.Marshal(tensors)
	if err != nil {
		t.Fatal(err)
	}
	var lb [8]byte
	binary.LittleEndian.PutUint64(lb[:], uint64(len(header)))
	if err := os.WriteFile(path, append(append(lb[:], header...), 'w'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadSafetensorsHeader(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.safetensors")
	safetensorsFile(t, p, map[string]any{
		"model.layers.0.weight": map[string]any{"dtype": "F16", "shape": []int{4}},
		"__metadata__":          map[string]string{"format": "pt"},
	})
	names, err := ReadSafetensorsHeader(p)
	if err != nil {
		t.Fatalf("ReadSafetensorsHeader: %v", err)
	}
	if len(names) != 1 || names[0] != "model.layers.0.weight" {
		t.Errorf("names = %v, want the one tensor, metadata excluded", names)
	}
	// A length field that names no header is not a header.
	bad := filepath.Join(dir, "bad.safetensors")
	var lb [8]byte
	binary.LittleEndian.PutUint64(lb[:], maxHeader+1)
	if err := os.WriteFile(bad, lb[:], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSafetensorsHeader(bad); err == nil {
		t.Error("an oversized header length must be refused")
	}
	if _, err := ReadSafetensorsHeader(filepath.Join(dir, "nope.safetensors")); err == nil {
		t.Error("a missing file must be an error")
	}
}

func TestClassifyTensors(t *testing.T) {
	for _, tc := range []struct {
		names []string
		want  Shelf
	}{
		{[]string{"lora_unet_down_blocks.0.weight"}, ShelfImage},
		{[]string{"model.diffusion_model.blocks.0.weight"}, ShelfImage},
		{[]string{"first_stage_model.encoder.weight"}, ShelfImage},
		{[]string{"text_model.encoder.weight"}, ShelfImage},
		{[]string{"language_model.layers.0.weight"}, ShelfChat},
		{[]string{"model.layers.0.mlp.weight"}, ShelfChat},
		{[]string{"wte.weight", "attn.weight"}, ""},
		{nil, ""},
	} {
		if got := ClassifyTensors(tc.names); got != tc.want {
			t.Errorf("ClassifyTensors(%v) = %q, want %q", tc.names, got, tc.want)
		}
	}
}

func TestShelfOfSafetensorsDir(t *testing.T) {
	mk := func(t *testing.T, tensors map[string]any, cfg string) Shelf {
		t.Helper()
		dir := t.TempDir()
		// The scan takes a safetensors set only with a config.json beside it.
		if cfg != "" {
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		safetensorsFile(t, filepath.Join(dir, "weights.safetensors"), tensors)
		ms := Scan([]string{dir})
		if len(ms) != 1 {
			t.Fatalf("scan = %d models, want 1: %+v", len(ms), ms)
		}
		return ms[0].Shelf
	}
	if s := mk(t, map[string]any{"model.diffusion_model.x": map[string]any{"dtype": "F16"}}, `{"model_type":"stable-diffusion"}`); s != ShelfImage {
		t.Errorf("config stable-diffusion = %q, want image", s)
	}
	if s := mk(t, map[string]any{"model.diffusion_model.x": map[string]any{"dtype": "F16"}}, `{"model_type":"llama"}`); s != ShelfChat {
		t.Errorf("config llama = %q, want chat (the config outranks the tensors)", s)
	}
	if s := mk(t, map[string]any{"lora_down.weight": map[string]any{"dtype": "F16"}}, `{}`); s != ShelfImage {
		t.Errorf("configless lora tensors = %q, want image from the header", s)
	}
}

func TestApplyRootShelves(t *testing.T) {
	ms := []Model{
		{Path: "/mnt/windows/ComfyUI/models/checkpoints/x.safetensors", Shelf: ShelfChat},
		{Path: "/mnt/windows/other/y.gguf", Shelf: ShelfAudio},
	}
	ApplyRootShelves(ms, []RootShelf{
		{Path: "/mnt/windows", Shelf: ShelfImage},
		{Path: "/mnt/windows/ComfyUI/models", Shelf: ShelfAudio},
	})
	// The deeper declaration wins for the ComfyUI file.
	if ms[0].Shelf != ShelfAudio {
		t.Errorf("ComfyUI file = %q, want the deeper root's audio", ms[0].Shelf)
	}
	if ms[1].Shelf != ShelfImage {
		t.Errorf("other file = %q, want the shallow root's image", ms[1].Shelf)
	}
}

func TestShelfTotals(t *testing.T) {
	got := ShelfTotals([]Model{
		{Shelf: ShelfChat, Bytes: 100},
		{Shelf: ShelfChat, Bytes: 50},
		{Shelf: ShelfImage, Bytes: 7},
		{Bytes: 3},
	})
	if got[ShelfChat] != 150 || got[ShelfImage] != 7 || got[""] != 3 {
		t.Errorf("totals = %+v", got)
	}
}
