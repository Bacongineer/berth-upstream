package cli

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ggufStr writes a GGUF string: a u64 length and the bytes.
func ggufStr(b *strings.Builder, s string) {
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], uint64(len(s)))
	b.WriteString(string(n[:]))
	b.WriteString(s)
}

// writeTestGGUF lays down a header the scanner can read: one metadata pair
// naming the architecture, no tensors, and zero padding after.
func writeTestGGUF(t *testing.T, dir, name, arch string, size int) string {
	t.Helper()
	b := &strings.Builder{}
	b.WriteString("GGUF")
	var u [4]byte
	binary.LittleEndian.PutUint32(u[:], 3) // version
	b.WriteString(string(u[:]))
	var z [8]byte
	b.WriteString(string(z[:])) // tensor count 0
	binary.LittleEndian.PutUint64(z[:], 1)
	b.WriteString(string(z[:])) // one metadata pair
	ggufStr(b, "general.architecture")
	binary.LittleEndian.PutUint32(u[:], 8) // typeString
	b.WriteString(string(u[:]))
	ggufStr(b, arch)
	out := b.String()
	if len(out) < size {
		out += strings.Repeat("\x00", size-len(out))
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStorageCommand(t *testing.T) {
	home := isolate(t)
	root := t.TempDir()
	writeTestGGUF(t, root, "llama-3.1-8b.Q4_K_M.gguf", "llama", 64<<10)
	imgDir := filepath.Join(root, "stable-diffusion")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestGGUF(t, imgDir, "sdXL-unet.gguf", "llama", 32<<10)
	writeTestGGUF(t, imgDir, "sdXL-vae.gguf", "llama", 16<<10)

	cfg := filepath.Join(home, "config", "berth", "berth.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	// The image files say llama in their header; the config says the root is
	// an image store, and that is what the operator meant.
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf(
		"[models]\ndirs = [\"%s\"]\n\n[library]\nfree_floor = 0.0\n\n[[library.root]]\npath = \"%s\"\nshelf = \"image\"\nshared = \"windows\"\n",
		root, root)), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runCLI(t, "storage", "--dir", root)
	if code != 0 {
		t.Fatalf("storage: code %d\nout: %s\nerr: %s", code, out, errOut)
	}
	if !strings.Contains(out, "shared with windows") {
		t.Errorf("human output lost the shared-with line:\n%s", out)
	}
	if !strings.Contains(out, "floor ok") {
		t.Errorf("a zero floor must read ok:\n%s", out)
	}
	code, out, errOut = runCLI(t, "storage", "--dir", root, "--format", "json")
	if code != 0 {
		t.Fatalf("json: code %d\n%s", code, errOut)
	}
	var doc storageDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Status != "ok" || len(doc.Roots) != 1 {
		t.Fatalf("doc = %+v", doc)
	}
	r := doc.Roots[0]
	if r.FS == "" {
		t.Error("no filesystem name on the root")
	}
	if r.Shared != "windows" {
		t.Errorf("shared = %q, want windows", r.Shared)
	}
	// The config's image shelf beats the files' llama header.
	if u := r.Shelves["image"]; u == nil || u.Models != 3 {
		t.Errorf("image shelf = %+v, want all three models", u)
	}
	if u := r.Shelves["image"]; u != nil && u.Bytes != (64+32+16)<<10 {
		t.Errorf("image bytes = %d, want %d", u.Bytes, (64+32+16)<<10)
	}
}

func TestStorageUnderFloorFails(t *testing.T) {
	home := isolate(t)
	root := t.TempDir()
	// A file on the root: a root that holds nothing has nothing used.
	if err := os.WriteFile(filepath.Join(root, "a.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(home, "config", "berth", "berth.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	// A floor of 100% of the root: nothing on the box is that empty.
	if err := os.WriteFile(cfg, []byte(
		"[library]\nfree_floor = 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI(t, "storage", "--dir", root, "--format", "json")
	if code == 0 {
		t.Fatalf("a full floor must fail the check\n%s", out)
	}
	var doc storageDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Status != "failed" || len(doc.Roots) == 0 || !doc.Roots[0].UnderFloor {
		t.Errorf("doc = %+v, want failed with the root under floor", doc)
	}
	code, out, errOut = runCLI(t, "storage", "--dir", root)
	if code == 0 || !strings.Contains(out, "under floor") {
		t.Errorf("human run code %d lost the verdict\n%s%s", code, out, errOut)
	}
}
