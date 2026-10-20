// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"strings"
	"testing"
)

type argv struct {
	name string
	args []string
}

func (a argv) String() string { return a.name + " " + strings.Join(a.args, " ") }

// fakeOps is serviceOps with a recorded runner: a test never reaches
// systemctl or schtasks, and the platform's files land in dir. respond
// answers each call; nil answers nothing.
func fakeOps(t *testing.T, dir string, respond func(argv) (string, error)) (serviceOps, *[]argv) {
	t.Helper()
	if respond == nil {
		respond = func(argv) (string, error) { return "", nil }
	}
	var calls []argv
	ops := serviceOps{
		run: func(ctx context.Context, name string, args ...string) (string, error) {
			call := argv{name, args}
			calls = append(calls, call)
			return respond(call)
		},
		unitDir: func() (string, error) { return dir, nil },
	}
	return ops, &calls
}

func TestServiceUnits(t *testing.T) {
	const exe = "/opt/berth/berth"
	one := serviceUnits(exe, false, false)
	if len(one) != 1 || one[0].Name != "berthd" {
		t.Fatalf("the node is always in the list, got %+v", one)
	}
	if got := one[0].Exec; len(got) != 6 || got[0] != exe || got[1] != "daemon" {
		t.Errorf("the node's command is the user's binary as daemon, got %v", got)
	}

	all := serviceUnits(exe, true, true)
	if len(all) != 3 {
		t.Fatalf("node, gateway and harbor, got %+v", all)
	}
	wantExec := map[string][]string{
		"berthd":        {exe, "daemon", "--gpu", "0", "--poll", "2s"},
		"berth-gateway": {exe, "gateway", "--listen", "127.0.0.1:9799"},
		"berth-harbor":  {exe, "harbor", "--listen", "127.0.0.1:9798"},
	}
	for _, u := range all {
		if want, ok := wantExec[u.Name]; !ok || !sameArgs(u.Exec, want) {
			t.Errorf("Exec of %s = %v, want %v", u.Name, u.Exec, wantExec[u.Name])
		}
		if u.Name != "berthd" && !sameArgs(u.After, []string{"berthd"}) {
			t.Errorf("%s comes up after the node, got %v", u.Name, u.After)
		}
	}

	if u := serviceUnits(exe, true, false)[1]; u.Name != "berth-gateway" {
		t.Errorf("--gateway selects the gateway, got %s", u.Name)
	}
	if u := serviceUnits(exe, false, true)[1]; u.Name != "berth-harbor" {
		t.Errorf("--harbor selects the harbor, got %s", u.Name)
	}
}

func sameArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
