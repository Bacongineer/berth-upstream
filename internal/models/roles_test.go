// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package models

import (
	"fmt"
	"path/filepath"
	"testing"
)

// fileGGUF writes one GGUF whose header says exactly these things, and returns
// its path so a scan can be pointed at it.
func fileGGUF(t *testing.T, path string, kvs []kv, ts []tensor) string {
	t.Helper()
	writeGGUF(t, path, buildGGUF(t, 3, kvs, ts))
	return path
}

func TestRoleOf(t *testing.T) {
	cases := []struct {
		arch, path, want string
	}{
		{"llama", "/m/qwen3-8b-q4_k_m.gguf", RoleModel},
		{"clip", "/m/vit.gguf", RoleMMProj},               // llama.cpp's vision encoder
		{"qwen2", "/m/qwen2.5-vl-mmproj.f16", RoleMMProj}, // the name says it
		{"qwen2", "/m/Qwen2.5-VL-7B-Instruct-q8_0.gguf", RoleModel},
		{"qwen3mtp", "/m/qwen3-8b.gguf", RoleDraft}, // the arch is the draft's
		{"llama", "/m/DeepSeek-Coder-V2-eagle-Qwen2.5-7B.gguf", RoleDraft},
		{"llama", "/m/gemma-3-12b-dflash.gguf", RoleDraft},
		// A bare mtp in a name is a model with its MTP head, not a draft: see refineDrafts.
		{"llama", "/m/MyModel-MTP-gguf.gguf", RoleModel},
		{"llama", "/m/MyModel-FastMTP-32K.gguf", RoleDraft},
		{"qwen4exp-mtp", "/m/mtp-q2_0.gguf", RoleDraft},
		{"medusa", "/m/head.gguf", RoleDraft},
		{"bert", "/m/bge-m3.gguf", RoleEmbedding},
		{"nomic-bert", "/m/nomic-embed-text.gguf", RoleEmbedding},
		{"gemma-embedding-300m", "/m/gemma-embed.gguf", RoleEmbedding},
		{"bge-m3-embedding", "/m/whatever.gguf", RoleEmbedding},
		{"", "/m/nameless.gguf", RoleModel}, // a header that will not read
	}
	for _, tc := range cases {
		if got := RoleOf(tc.arch, tc.path); got != tc.want {
			t.Errorf("RoleOf(%q, %q) = %s, want %s", tc.arch, tc.path, got, tc.want)
		}
	}
	if got := RoleForArch("bert"); got != RoleEmbedding {
		t.Errorf("RoleForArch(bert) = %s, want %s", got, RoleEmbedding)
	}
}

func TestFileTypeNames(t *testing.T) {
	// A number llama.cpp states is named; one it does not is the dominant
	// tensor type, never an invented name.
	known := fileGGUF(t, filepath.Join(t.TempDir(), "known.gguf"),
		[]kv{{"general.architecture", "llama"}, {"general.file_type", uint32(15)}},
		[]tensor{{"a", []uint64{4, 4}, 14}, {"b", []uint64{4, 4}, 1}})
	g, err := ReadGGUF(known)
	if err != nil {
		t.Fatal(err)
	}
	if got := g.FileType(); got != "Q4_K_M" {
		t.Errorf("file_type 15 = %q, want Q4_K_M", got)
	}
	// The dominant type is the one most of the parameters are in: a real quant
	// has a handful of small F16 norm vectors and its weights in Q6_K, and the
	// weights are what the file is.
	unknown := fileGGUF(t, filepath.Join(t.TempDir(), "unknown.gguf"),
		[]kv{{"general.architecture", "llama"}, {"general.file_type", uint32(99)}},
		[]tensor{
			{"a", []uint64{4096, 4096}, 14},
			{"b", []uint64{4096, 4096}, 14},
			{"norm", []uint64{4, 4}, 1},
		})
	g, err = ReadGGUF(unknown)
	if err != nil {
		t.Fatal(err)
	}
	if got := g.FileType(); got != "Q6_K" {
		t.Errorf("file_type 99 = %q, want the dominant tensor type Q6_K", got)
	}
	// The two numbers that were wrong once: 38 is MXFP4_MOE, and 34 is a
	// removed repack variant with no name to give it.
	for ftype, want := range map[uint32]string{38: "MXFP4_MOE", 36: "TQ1_0", 37: "TQ2_0"} {
		p := fileGGUF(t, filepath.Join(t.TempDir(), "ft.gguf"),
			[]kv{{"general.architecture", "llama"}, {"general.file_type", ftype}},
			[]tensor{{"a", []uint64{4, 4}, 1}})
		g, err := ReadGGUF(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := g.FileType(); got != want {
			t.Errorf("file_type %d = %q, want %s", ftype, got, want)
		}
	}
	removed := fileGGUF(t, filepath.Join(t.TempDir(), "removed.gguf"),
		[]kv{{"general.architecture", "llama"}, {"general.file_type", uint32(34)}},
		[]tensor{{"a", []uint64{4, 4}, 1}})
	g, err = ReadGGUF(removed)
	if err != nil {
		t.Fatal(err)
	}
	if got := g.FileType(); got != "F16" {
		t.Errorf("file_type 34 = %q, want the tensors' own F16 and not a guess", got)
	}
}

// TestScanRolesAndCompanions is the library as it is really stored: a model, a
// second quant of it, its vision projector, a draft, and an embedding model
// beside them.
func TestScanRolesAndCompanions(t *testing.T) {
	dir := t.TempDir()
	main := fileGGUF(t, filepath.Join(dir, "Qwen2.5-VL-7B-Q4_K_M.gguf"),
		[]kv{{"general.architecture", "qwen2"}, {"general.name", "Qwen2.5 VL 7B"}},
		[]tensor{{"a", []uint64{1024, 1024}, 1}})
	fileGGUF(t, filepath.Join(dir, "Qwen2.5-VL-7B-Q8_0.gguf"),
		[]kv{{"general.architecture", "qwen2"}, {"general.name", "Qwen2.5 VL 7B"}},
		[]tensor{{"a", []uint64{1024, 1024}, 1}})
	mmproj := fileGGUF(t, filepath.Join(dir, "Qwen2.5-VL-7B-mmproj-f16.gguf"),
		[]kv{{"general.architecture", "clip"}},
		[]tensor{{"a", []uint64{64, 64}, 1}})
	emb := fileGGUF(t, filepath.Join(dir, "bge-m3.gguf"),
		[]kv{{"general.architecture", "bert"}},
		[]tensor{{"a", []uint64{64, 64}, 1}})

	ms := Scan([]string{dir})
	if len(ms) != 4 {
		t.Fatalf("scan found %d models, want 4: %s", len(ms), dump(ms))
	}
	byName := func(p string) Model {
		t.Helper()
		m, ok := byPath(ms, p)
		if !ok {
			t.Fatalf("no %s in %s", p, dump(ms))
		}
		return m
	}
	if got := byName(mmproj).Role; got != RoleMMProj {
		t.Errorf("mmproj role = %s, want %s", got, RoleMMProj)
	}
	if got := byName(emb).Role; got != RoleEmbedding {
		t.Errorf("embedding role = %s, want %s", got, RoleEmbedding)
	}
	vl := byName(main)
	if vl.Name != "Qwen2.5-VL-7B-Q4_K_M" {
		t.Errorf("name = %q, want the file stem", vl.Name)
	}
	if vl.Title != "Qwen2.5 VL 7B" {
		t.Errorf("title = %q, want the header's general.name", vl.Title)
	}
	// Two quants, one model (one general.name), one projector: it goes with both,
	// because that is what a directory of quants means.
	if len(vl.Companions) != 1 || filepath.Base(vl.Companions[0]) != "Qwen2.5-VL-7B-mmproj-f16.gguf" {
		t.Errorf("companions = %v, want the projector this directory ships", vl.Companions)
	}
	if q8 := byName(filepath.Join(dir, "Qwen2.5-VL-7B-Q8_0.gguf")); len(q8.Companions) != 1 {
		t.Errorf("the other quant's companions = %v, want the same projector", q8.Companions)
	}

	// Two different models in one directory, and the adapter has no owner.
	two := t.TempDir()
	fileGGUF(t, filepath.Join(two, "a-q4.gguf"),
		[]kv{{"general.architecture", "qwen2"}, {"general.name", "Model A"}}, []tensor{{"a", []uint64{16, 16}, 1}})
	fileGGUF(t, filepath.Join(two, "b-q4.gguf"),
		[]kv{{"general.architecture", "qwen2"}, {"general.name", "Model B"}}, []tensor{{"a", []uint64{16, 16}, 1}})
	fileGGUF(t, filepath.Join(two, "mmproj.gguf"),
		[]kv{{"general.architecture", "clip"}}, []tensor{{"a", []uint64{16, 16}, 1}})
	for _, m := range Scan([]string{two}) {
		if len(m.Companions) != 0 {
			t.Errorf("%s carries %v: two models share the directory, so which one the projector "+
				"belongs to is a guess", m.Name, m.Companions)
		}
	}
	// The listing is what a person runs a model from: mains and embeddings,
	// with the companions counted rather than listed.
	if got := len(Main(ms)); got != 3 {
		t.Errorf("Main = %d models, want 3 (two quants and the embedder): %s", got, dump(Main(ms)))
	}

	// One main in the directory, and the projector has somewhere to go.
	one := t.TempDir()
	fileGGUF(t, filepath.Join(one, "Qwen2.5-VL-7B-Q4_K_M.gguf"),
		[]kv{{"general.architecture", "qwen2"}}, []tensor{{"a", []uint64{1024, 1024}, 1}})
	fileGGUF(t, filepath.Join(one, "mmproj-f16.gguf"),
		[]kv{{"general.architecture", "clip"}}, []tensor{{"a", []uint64{64, 64}, 1}})
	fileGGUF(t, filepath.Join(one, "draft.gguf"),
		[]kv{{"general.architecture", "llama"}}, []tensor{{"a", []uint64{64, 64}, 1}})
	ms = Scan([]string{one})
	got := ms[0]
	if got.Name != "Qwen2.5-VL-7B-Q4_K_M" {
		// Sorted by name, so the draft would otherwise be first.
		got = ms[1]
	}
	if len(got.Companions) != 2 {
		t.Fatalf("companions = %v, want the projector and the draft", got.Companions)
	}
	want := []string{filepath.Join(one, "draft.gguf"), filepath.Join(one, "mmproj-f16.gguf")}
	for i := range want {
		if got.Companions[i] != want[i] {
			t.Errorf("companions = %v, want %v", got.Companions, want)
		}
	}
	if m := len(Main(ms)); m != 1 {
		t.Errorf("Main = %d, want the one model the companions belong to", m)
	}
}

// TestRefineDrafts is the rule the real library forced: the same MTP in a file
// name means a draft on a 1.9 GB head and a whole model on a 27 GB file, and
// only the size beside its neighbours can tell them apart.
func TestRefineDrafts(t *testing.T) {
	dir := t.TempDir()
	fileGGUF(t, filepath.Join(dir, "Qwen3.8-27B-IQ4_XS.gguf"),
		[]kv{{"general.architecture", "qwen35"}}, []tensor{{"w", []uint64{27000, 1000}, 14}})
	head := fileGGUF(t, filepath.Join(dir, "Qwen3.8-27B-FastMTP-32K.gguf"),
		[]kv{{"general.architecture", "qwen35"}}, []tensor{{"w", []uint64{100, 100}, 14}})
	// Its own directory: the biggest file in there has nothing to be small beside.
	own := t.TempDir()
	ownName := fileGGUF(t, filepath.Join(own, "mtp-Qwen3.8-35B-distill-Q4_K_M.gguf"),
		[]kv{{"general.architecture", "qwen35moe"}}, []tensor{{"w", []uint64{1900, 1000}, 14}})
	ms := Scan([]string{dir})
	role := map[string]string{}
	for _, m := range ms {
		role[filepath.Base(m.Path)] = m.Role
	}
	if role[filepath.Base(head)] != RoleDraft {
		t.Errorf("FastMTP head role = %s, want draft (1%% of the model beside it)", role[filepath.Base(head)])
	}
	// A file whose own name starts mtp- but which is the biggest thing in its
	// directory has no evidence to call it a draft, so it stays a model: listing
	// a draft is a smaller mistake than hiding a model.
	if got := Scan([]string{own}); len(got) != 1 || got[0].Role != RoleModel {
		t.Errorf("the biggest mtp-named file beside nobody = %+v, want one model (%s)", got, ownName)
	}
	if role["Qwen3.8-27B-IQ4_XS.gguf"] != RoleModel {
		t.Errorf("the model itself = %s, want model", role["Qwen3.8-27B-IQ4_XS.gguf"])
	}
	// And the head is attached to the model it is small beside.
	for _, m := range ms {
		if m.Name == "Qwen3.8-27B-IQ4_XS" && len(m.Companions) != 1 {
			t.Errorf("companions = %v, want the head beside it", m.Companions)
		}
	}
}

// TestScanSplitParams is the parameter count of a split model: every part holds
// part of the weights, so no single header states the size of the model.
func TestScanSplitParams(t *testing.T) {
	dir := t.TempDir()
	part := func(n int, dims uint64) string {
		return fileGGUF(t, filepath.Join(dir, sprintfPart(n)),
			[]kv{
				{"general.architecture", "llama"},
				{"general.name", "Split"},
				{"split.count", uint32(2)},
			},
			[]tensor{{"a", []uint64{dims, 64}, 1}})
	}
	part(1, 1000)
	part(2, 1500)
	ms := Scan([]string{dir})
	if len(ms) != 1 {
		t.Fatalf("scan found %d models, want 1: %s", len(ms), dump(ms))
	}
	m := ms[0]
	if want := uint64(2500 * 64); m.Params != want {
		t.Errorf("params = %d, want every part summed (%d)", m.Params, want)
	}
	if m.Name != "model" {
		t.Errorf("name = %q, want the stem without the split suffix", m.Name)
	}
	if m.Parts != 2 || len(m.Files) != 2 {
		t.Errorf("parts = %d over %d files, want 2 and 2", m.Parts, len(m.Files))
	}
	if m.Title != "Split" {
		t.Errorf("title = %q, want the header's name", m.Title)
	}
}

func sprintfPart(n int) string { return fmt.Sprintf("model-0000%d-of-00002.gguf", n) }

// TestStem is the naming rule the whole CLI keys on.
func TestStem(t *testing.T) {
	cases := map[string]string{
		"/models/Qwen3.8-27B-IQ4_XS.gguf":        "Qwen3.8-27B-IQ4_XS",
		"/models/model-00001-of-00003.gguf":      "model",
		"/models/deep.think.Qwen-7B.Q4_K_M.gguf": "deep.think.Qwen-7B.Q4_K_M",
		"/models/mmproj-BF16":                    "mmproj-BF16",
		// The suffix goes whatever it is; the split rule is only ever a .gguf.
		"/models/model-00001-of-00003.guff": "model-00001-of-00003",
		"/models/no-suffix":                 "no-suffix",
	}
	for p, want := range cases {
		if got := Stem(p); got != want {
			t.Errorf("Stem(%q) = %q, want %q", p, got, want)
		}
	}
}
