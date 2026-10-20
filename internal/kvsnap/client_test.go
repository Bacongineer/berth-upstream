// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package kvsnap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// engine returns a server that answers every request with status and body,
// recording the last request's path, query and body.
func engine(t *testing.T, status int, body string) (*httptest.Server, *captured) {
	t.Helper()
	c := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.path = r.URL.Path
		c.query = r.URL.RawQuery
		c.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

type captured struct {
	path, query string
	body        []byte
}

func TestSlots(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		want    []Slot
		wantErr string
	}{
		{
			name:   "busy and idle slots, the token count only on the busy one",
			status: 200,
			body:   `[{"id":0,"state":"idle","n_ctx":32768},{"id":1,"state":"processing","n_prompt_tokens":4217,"n_ctx":32768,"params":{},"next_token":{}}]`,
			want:   []Slot{{ID: 0}, {ID: 1, Tokens: 4217, Busy: true}},
		},
		{
			name:   "the NInfer bool counts as busy",
			status: 200,
			body:   `[{"id":2,"is_processing":true,"n_prompt_tokens":99}]`,
			want:   []Slot{{ID: 2, Tokens: 99, Busy: true}},
		},
		{
			name:    "a build without the endpoint stays a plain error",
			status:  501,
			body:    `{"error":{"code":501,"message":"This server does not support slots endpoint.","type":"not_supported_error"}}`,
			wantErr: "GET",
		},
		{
			name:    "a 404 is a plain error too",
			status:  404,
			body:    `not found`,
			wantErr: "GET",
		},
		{
			name:    "garbage on a 200 is a plain error",
			status:  200,
			body:    `<html>nope</html>`,
			wantErr: "GET",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, c := engine(t, tc.status, tc.body)
			got, err := Slots(context.Background(), srv.URL)
			if c.path != "/slots" {
				t.Errorf("requested %s, want /slots", c.path)
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want a %s error", err, tc.wantErr)
				}
				if errors.Is(err, ErrUnsupported) {
					t.Errorf("a missing /slots endpoint is not ErrUnsupported: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Slots: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d slots, want %d", len(got), len(tc.want))
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("slot %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestSlotsTrailingSlash(t *testing.T) {
	srv, _ := engine(t, 200, `[{"id":0,"state":"idle"}]`)
	if _, err := Slots(context.Background(), srv.URL+"/"); err != nil {
		t.Fatalf("Slots with a trailing slash: %v", err)
	}
}

func TestSave(t *testing.T) {
	cases := []struct {
		name          string
		status        int
		body          string
		want          Result
		wantUnsupp    bool
		wantErrSubstr string
	}{
		{
			name:   "the engine's own numbers",
			status: 200,
			body:   `{"id_slot":3,"filename":"0123456789abcdef_s3","n_saved":4217,"n_written":81234567,"timings":{"save_ms":49.865}}`,
			want:   Result{Tokens: 4217, Bytes: 81234567, Took: 49*time.Millisecond + 865*time.Microsecond},
		},
		{
			name:       "a 400 maps to ErrUnsupported",
			status:     400,
			body:       `{"error":{"code":400,"message":"Invalid slot ID","type":"invalid_request_error"}}`,
			wantUnsupp: true,
		},
		{
			name:       "a 404 maps to ErrUnsupported",
			status:     404,
			body:       `no such endpoint`,
			wantUnsupp: true,
		},
		{
			name:       "a 501 maps to ErrUnsupported",
			status:     501,
			body:       `{"error":{"code":501,"message":"This server does not support slots action. Start it with --slot-save-path","type":"not_supported_error"}}`,
			wantUnsupp: true,
		},
		{
			name:       "an error body that says saving is disabled",
			status:     500,
			body:       `{"error":{"code":500,"message":"This server does not support slots action. Start it with --slot-save-path","type":"server_error"}}`,
			wantUnsupp: true,
		},
		{
			name:          "any other failure is plain and names the endpoint",
			status:        500,
			body:          `{"error":{"code":500,"message":"save failed: disk full","type":"server_error"}}`,
			wantErrSubstr: "/slots/3?action=save",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, c := engine(t, tc.status, tc.body)
			got, err := Save(context.Background(), srv.URL, 3, "0123456789abcdef_s3")
			if c.path != "/slots/3" || c.query != "action=save" {
				t.Errorf("request = %s?%s, want /slots/3?action=save", c.path, c.query)
			}
			var sent struct {
				Filename string `json:"filename"`
			}
			if json.Unmarshal(c.body, &sent) != nil || sent.Filename != "0123456789abcdef_s3" {
				t.Errorf("sent body %s, want the filename", c.body)
			}
			switch {
			case tc.wantUnsupp:
				if !errors.Is(err, ErrUnsupported) {
					t.Fatalf("err = %v, want ErrUnsupported", err)
				}
			case tc.wantErrSubstr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Fatalf("err = %v, want one naming %s", err, tc.wantErrSubstr)
				}
				if errors.Is(err, ErrUnsupported) {
					t.Errorf("a plain failure must not be ErrUnsupported: %v", err)
				}
			default:
				if err != nil {
					t.Fatalf("Save: %v", err)
				}
				if got != tc.want {
					t.Errorf("result = %+v, want %+v", got, tc.want)
				}
			}
		})
	}
}

func TestRestore(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		want       Result
		wantUnsupp bool
	}{
		{
			name:   "the engine's own numbers",
			status: 200,
			body:   `{"id_slot":0,"filename":"0123456789abcdef_s0","n_restored":4217,"n_read":81234567,"timings":{"restore_ms":42.937}}`,
			want:   Result{Tokens: 4217, Bytes: 81234567, Took: 42*time.Millisecond + 937*time.Microsecond},
		},
		{
			name:       "a 501 maps to ErrUnsupported",
			status:     501,
			body:       `{"error":{"code":501,"message":"This server does not support slots action. Start it with --slot-save-path","type":"not_supported_error"}}`,
			wantUnsupp: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, c := engine(t, tc.status, tc.body)
			got, err := Restore(context.Background(), srv.URL, 0, "0123456789abcdef_s0")
			if c.path != "/slots/0" || c.query != "action=restore" {
				t.Errorf("request = %s?%s, want /slots/0?action=restore", c.path, c.query)
			}
			if tc.wantUnsupp {
				if !errors.Is(err, ErrUnsupported) {
					t.Fatalf("err = %v, want ErrUnsupported", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Restore: %v", err)
			}
			if got != tc.want {
				t.Errorf("result = %+v, want %+v", got, tc.want)
			}
		})
	}
}
