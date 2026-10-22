// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package models

import (
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Shelf is the kind of engine that loads a model. Text is what a language
// engine (llama.cpp, vLLM, Ollama) loads: chat models, and also embedders,
// rerankers and vision or OCR models (issue 148). Image is what a diffusion
// engine loads, audio what a speech engine loads. The empty shelf is no
// shelf: Berth could not tell, and it says so instead of guessing.
type Shelf string

const (
	ShelfText  Shelf = "text"
	ShelfImage Shelf = "image"
	ShelfAudio Shelf = "audio"
)

// Shelves is the declared set, for validation and listing order.
var Shelves = []Shelf{ShelfText, ShelfImage, ShelfAudio}

// ParseShelf reads a shelf from a config or a flag. "chat", the text
// shelf's name before v0.3.4, still reads as text, so older configs keep
// working.
func ParseShelf(s string) (Shelf, bool) {
	if s == "chat" {
		return ShelfText, true
	}
	return Shelf(s), Shelf(s).Valid()
}

// Valid is whether s names one of the shelves.
func (s Shelf) Valid() bool {
	for _, x := range Shelves {
		if s == x {
			return true
		}
	}
	return false
}

// imageGGUFArchs are the architectures a diffusion model reports inside a
// GGUF. Every other readable GGUF architecture is one llama.cpp loads, which
// is what the format exists for, so it goes on the text shelf. A
// safetensors model_type has no such default: the format carries any kind
// of model, so a type no table names stays unknown.
var imageGGUFArchs = map[string]bool{
	"stable_diffusion": true, "stable-diffusion": true, "sdxl": true, "sd3": true,
	"sd35": true, "flux": true, "stable_cascade": true, "omnigen": true, "wan": true,
}

// audioGGUFArchs are the architectures that produce speech, not text.
var audioGGUFArchs = map[string]bool{"whisper": true}

// imageModelTypes are the config.json model_type values of diffusion models
// and of the assets that go with them.
var imageModelTypes = map[string]bool{
	"stable-diffusion": true, "sd3": true, "flux": true, "lora": true, "sana": true,
	"omnigen": true, "chroma": true,
}

// audioModelTypes are the config.json model_type values of speech models.
var audioModelTypes = map[string]bool{"whisper": true}

// chatModelTypes are the config.json model_type values of language models,
// the ones a language engine loads. A value not in any of the three tables
// stays unknown.
var chatModelTypes = map[string]bool{
	"llama": true, "mistral": true, "mixtral": true, "qwen": true, "gemma": true,
	"gpt2": true, "gpt_neox": true, "gptj": true, "phi": true, "phi3": true,
	"phi4": true, "mamba": true, "mpt": true, "bert": true, "roberta": true,
	"xlm-roberta": true, "codestral": true, "starcoder2": true, "falcon": true,
	"stablelm": true, "granite": true, "olmo": true, "baichuan": true, "yi": true,
	"bge": true, "gte": true, "jina": true, "t5": true, "codegen": true, "seahorse": true,
}

// audioNameWords are file and directory names that state a speech model.
var audioNameWords = []string{"whisper", "kokoro", "piper", "faster-whisper", "voice", "tts"}

// imageNameWords are file and directory names that state an image asset.
var imageNameWords = []string{"lora", "vae", "upscaler", "unet", "sdxl", "flux", "stable-diffusion", "sd15", "sd3"}

// ShelfOf decides a model's shelf from what the file states, in order of
// authority: where it came from (Ollama and LM Studio stores hold language
// models), what its header says (a GGUF's architecture, a safetensors
// config, the tensors themselves), and what its name says. Nothing here is
// a guess: an undecided model comes back on no shelf.
func ShelfOf(m *Model) Shelf {
	switch m.Source {
	case SourceOllama, SourceLMStudio:
		return ShelfText
	}
	if m.Format == FormatGGUF && m.Arch != "" {
		a := strings.ToLower(m.Arch)
		if audioGGUFArchs[a] {
			return ShelfAudio
		}
		if imageGGUFArchs[a] {
			return ShelfImage
		}
		return ShelfText
	}
	if m.Format == FormatSafetensors && m.Arch != "" {
		a := strings.ToLower(m.Arch)
		if audioModelTypes[a] {
			return ShelfAudio
		}
		if imageModelTypes[a] {
			return ShelfImage
		}
		if chatModelTypes[a] {
			return ShelfText
		}
	}
	if s := shelfByName(m.Name, m.Path); s != "" {
		return s
	}
	// A safetensors set with no config states itself through its tensors.
	// Shards split one header, so the first files are tried in turn until
	// one of them decides.
	if m.Format == FormatSafetensors && m.Arch == "" {
		files := m.Files
		if len(files) == 0 {
			files = []string{m.Path}
		}
		for _, p := range files[:min(len(files), 3)] {
			names, err := ReadSafetensorsHeader(p)
			if err != nil {
				continue
			}
			if s := ClassifyTensors(names); s != "" {
				return s
			}
		}
	}
	return ""
}

// shelfByName reads a shelf off the model's name and its directory's name.
func shelfByName(name, path string) Shelf {
	wants := func(words []string) bool {
		low := func(s string) string { return strings.ToLower(s) }
		for _, w := range words {
			if strings.Contains(low(name), w) || strings.Contains(low(filepath.Base(filepath.Dir(path))), w) {
				return true
			}
		}
		return false
	}
	if wants(audioNameWords) {
		return ShelfAudio
	}
	if wants(imageNameWords) {
		return ShelfImage
	}
	return ""
}

// RootShelf is a shelf declared for a whole root in berth.toml: everything
// under the path has the shelf, which beats what a file states.
type RootShelf struct {
	Path  string
	Shelf Shelf
}

// ApplyRootShelves sets the declared shelf on every model that lies under a
// declared root. A model under two declared roots takes the deeper one.
func ApplyRootShelves(ms []Model, roots []RootShelf) {
	if len(roots) == 0 {
		return
	}
	sort.Slice(roots, func(i, j int) bool { return len(roots[i].Path) > len(roots[j].Path) })
	for i := range ms {
		for _, r := range roots {
			if r.Shelf == "" {
				continue
			}
			if underPath(ms[i].Path, r.Path) {
				ms[i].Shelf = r.Shelf
				break
			}
		}
	}
}

// underPath reports whether p lies under root. Both sides are cleaned,
// so a root typed with forward slashes matches on Windows too, and on
// Windows the comparison folds case, as the filesystem does.
func underPath(p, root string) bool {
	if root == "" {
		return false
	}
	p, root = filepath.Clean(p), filepath.Clean(root)
	if runtime.GOOS == "windows" {
		p, root = strings.ToLower(p), strings.ToLower(root)
	}
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}

// ShelfTotals is the space a shelf occupies in ms.
func ShelfTotals(ms []Model) map[Shelf]uint64 {
	out := map[Shelf]uint64{}
	for i := range ms {
		out[ms[i].Shelf] += ms[i].Bytes
	}
	return out
}
