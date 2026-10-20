// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelsShelves(t *testing.T) {
	home := isolate(t)
	chat := t.TempDir()
	img := t.TempDir()
	writeTestGGUF(t, chat, "llama-3.1-8b.Q4_K_M.gguf", "llama", 64<<10)
	// Both files say llama in the header; the config says img holds images.
	writeTestGGUF(t, img, "unet.gguf", "llama", 32<<10)
	writeTestGGUF(t, img, "vae.gguf", "llama", 16<<10)

	cfg := filepath.Join(home, "config", "berth", "berth.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf(
		"[models]\ndirs = [\"%s\", \"%s\"]\n\n[library]\nfree_floor = 0.0\n\n[[library.root]]\npath = \"%s\"\nshelf = \"image\"\n",
		tomlPath(chat), tomlPath(img), tomlPath(img))), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runCLI(t, "models", "--format", "json")
	if code != 0 {
		t.Fatalf("models: code %d\n%s%s", code, out, errOut)
	}
	var doc modelsDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Count != 3 {
		t.Errorf("count = %d, want 3", doc.Count)
	}
	if u := doc.Shelves["chat"]; u == nil || u.Models != 1 || u.Bytes != 64<<10 {
		t.Errorf("chat shelf = %+v, want one 64 MiB model", u)
	}
	if u := doc.Shelves["image"]; u == nil || u.Models != 2 || u.Bytes != (32+16)<<10 {
		t.Errorf("image shelf = %+v, want the config-declared two", u)
	}

	// The filter keeps only the shelf it is told to.
	code, out, errOut = runCLI(t, "models", "--shelf", "image", "--format", "json")
	if code != 0 {
		t.Fatalf("shelf filter: code %d\n%s%s", code, out, errOut)
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Count != 2 || doc.Shelf != "image" {
		t.Errorf("filtered doc: count %d shelf %q", doc.Count, doc.Shelf)
	}
	for _, m := range doc.Models {
		if m.Shelf != "image" {
			t.Errorf("the filter let %s (shelf %s) through", m.Name, m.Shelf)
		}
	}
	if code, _, _ := runCLI(t, "models", "--shelf", "video"); code == 0 {
		t.Error("an unknown shelf must fail")
	}

	// The human listing names the shelf it filtered on.
	code, out, errOut = runCLI(t, "models", "--shelf", "chat")
	if code != 0 {
		t.Fatalf("human: code %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, "SHELF") {
		t.Errorf("the listing lost the SHELF column:\n%s", out)
	}
	if code, out, _ = runCLI(t, "models", "--shelf", "audio"); code != 0 || !strings.Contains(out, "No models on the audio shelf") {
		t.Errorf("an empty shelf must say which shelf it is\n%s", out)
	}

	// The scan's snapshot went back into the roots it came from.
	for _, root := range []string{chat, img} {
		b, err := os.ReadFile(filepath.Join(root, ".berth", "catalog.json"))
		if err != nil {
			t.Errorf("no catalog in %s: %v", root, err)
			continue
		}
		var cat struct {
			Root   string `json:"root"`
			Models []struct {
				Name  string `json:"name"`
				Shelf string `json:"shelf"`
			} `json:"models"`
		}
		if err := json.Unmarshal(b, &cat); err != nil {
			t.Fatalf("catalog in %s did not parse: %v", root, err)
		}
		if root == img {
			if len(cat.Models) != 2 || cat.Models[0].Shelf != "image" {
				t.Errorf("image root's catalog = %+v, want both models on the image shelf", cat.Models)
			}
		}
	}
}
