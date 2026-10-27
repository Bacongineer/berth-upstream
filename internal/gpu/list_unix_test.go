// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package gpu

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// --gpu all opens what List finds: every NVIDIA card from one query.
func TestListEveryNvidiaCard(t *testing.T) {
	bin, _ := fakeTwoCards(t)
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	devs, err := List(context.Background(), Options{Backend: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 2 || devs[0].Index != 0 || devs[1].Index != 1 || devs[1].Name != "RTX 6000" {
		t.Fatalf("devices = %+v", devs)
	}
}
