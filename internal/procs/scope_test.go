// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package procs

// The cgroup shapes a Berth scope reading recognises: the scopes the
// gateway and `berth run` create, nested work under them, and what a
// process outside any Berth scope reads.

import (
	"strings"
	"testing"
)

func TestScopePath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
		ok   bool
	}{
		{
			name: "gateway engine scope",
			path: "/user.slice/user-1000.slice/user@1000.service/app.slice/berth-engine-strata-1a2b3c4d.scope",
			want: "/user.slice/user-1000.slice/user@1000.service/app.slice/berth-engine-strata-1a2b3c4d.scope",
			ok:   true,
		},
		{
			name: "berth run scope",
			path: "/user.slice/user-1000.slice/user@1000.service/app.slice/berth-run-ninfer-aabbccdd.scope",
			want: "/user.slice/user-1000.slice/user@1000.service/app.slice/berth-run-ninfer-aabbccdd.scope",
			ok:   true,
		},
		{
			name: "nested work under the scope",
			path: "/user.slice/user-1000.slice/user@1000.service/app.slice/berth-run-strata-0f0e0d0c.scope/work/1",
			want: "/user.slice/user-1000.slice/user@1000.service/app.slice/berth-run-strata-0f0e0d0c.scope",
			ok:   true,
		},
		{
			name: "nearest ancestor wins",
			path: "/user.slice/user-1000.slice/user@1000.service/app.slice/berth-engine-a-1.scope/berth-run-b-2.scope",
			want: "/user.slice/user-1000.slice/user@1000.service/app.slice/berth-engine-a-1.scope/berth-run-b-2.scope",
			ok:   true,
		},
		{
			name: "terminal session scope",
			path: "/user.slice/user-1000.slice/session-3.scope",
			want: "",
			ok:   false,
		},
		{
			name: "root",
			path: "/",
			want: "",
			ok:   false,
		},
		{
			name: "berth prefix without the scope suffix",
			path: "/user.slice/berth-run-x.service",
			want: "",
			ok:   false,
		},
		{
			name: "scope suffix without the berth prefix",
			path: "/user.slice/app-run-x.scope",
			want: "",
			ok:   false,
		},
		{
			name: "berth with nothing between",
			path: "/user.slice/berth-.scope",
			want: "",
			ok:   false,
		},
		{
			name: "a longer word is not a scope",
			path: "/user.slice/berth-x.scope.evil",
			want: "",
			ok:   false,
		},
		{
			name: "empty",
			path: "",
			want: "",
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := scopePath(tt.path)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("scopePath(%q) = %q, %v, want %q, %v", tt.path, got, ok, tt.want, tt.ok)
			}
		})
	}
}

const runScope = "/user.slice/user-1000.slice/user@1000.service/app.slice/berth-run-a-1a2b3c4d.scope"

func TestScopeID(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
		ok   bool
	}{
		{
			name: "unified v2 line",
			body: "0::" + runScope + "\n",
			want: runScope,
			ok:   true,
		},
		{
			name: "nested under the scope",
			body: "0::" + runScope + "/work/1\n",
			want: runScope,
			ok:   true,
		},
		{
			name: "v1 controllers above the v2 line",
			body: "12:blkio:/user.slice/user-1000.slice\n11:devices:/user.slice/user-1000.slice\n0::" + runScope + "\n",
			want: runScope,
			ok:   true,
		},
		{
			name: "cgroup v1 only reads empty",
			body: "12:blkio:/user.slice/user-1000.slice/user@1000.service/berth-run-a-1a2b3c4d.scope\n1:name=systemd:/user.slice/user-1000.slice\n",
			want: "",
			ok:   false,
		},
		{
			name: "v2 present but no berth scope",
			body: "0::/user.slice/user-1000.slice/user@1000.service/app.slice/app-org.gnome.Terminal.slice/vte-child.scope\n",
			want: "",
			ok:   false,
		},
		{
			name: "empty",
			body: "",
			want: "",
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := scopeID(strings.NewReader(tt.body))
			if got != tt.want || ok != tt.ok {
				t.Fatalf("scopeID(%q) = %q, %v, want %q, %v", tt.body, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestScopeMem(t *testing.T) {
	sample := `anon 2619891712
file 9172271104
kernel 48390144
kernel_stack 1179648
pagetables 13664256
shmem 220209152
file_mapped 8934305792
file_dirty 0
anon_thp 0
inactive_anon 2836119552
active_file 156430336
`
	m, ok := scopeMem(strings.NewReader(sample))
	want := Mem{Anon: 2619891712, File: 9172271104, Shmem: 220209152}
	if !ok || m != want {
		t.Fatalf("scopeMem = %+v, %v; want %+v, true", m, ok, want)
	}

	m, ok = scopeMem(strings.NewReader("file 9172271104\nshmem 220209152\n"))
	if ok || m != (Mem{}) {
		t.Fatalf("scopeMem without anon = %+v, %v; want zero, false", m, ok)
	}
}
