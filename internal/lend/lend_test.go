// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package lend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/T-Crypt/berth/internal/httpauth"
)

// seen is one request as the engine got it.
type seen struct {
	Method, Path, Type, Auth, Body string
}

// engine answers every request with status and body, and records it.
func engine(t *testing.T, status int, body string) (*httptest.Server, *[]seen) {
	t.Helper()
	got := []seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, seen{r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Authorization"), string(b)})
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestProtocolLend(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		want   Ticket
		err    error
	}{
		{"lent", 200, `{"id":"1","gpu":"00000000:09:00.0","mib":8208}`, Ticket{ID: "1", GPU: "0000:09:00.0", MiB: 8208}, nil},
		{"too big", 409, `{"max_mib":10320}`, Ticket{}, &TooBigError{MaxMiB: 10320}},
		{"pending", 503, `{"error":"the engine did not answer LEND in 30 s"}`, Ticket{}, ErrPending},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, got := engine(t, c.status, c.body)
			a, ok := For("maya", "", srv.URL+"/")
			if !ok {
				t.Fatal("a lend_url is an adapter")
			}
			ctx := httpauth.WithBearer(context.Background(), "k")
			tk, err := a.Lend(ctx, Ask{GPU: "0000:09:00.0", MiB: 8192, FreeMiB: 900})
			if !reflect.DeepEqual(err, c.err) && !errors.Is(err, c.err) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			if tk != c.want {
				t.Errorf("ticket = %+v, want %+v", tk, c.want)
			}
			want := seen{"POST", "/vram/lend", "application/json", "Bearer k", `{"gpu":"0000:09:00.0","mib":8192}`}
			if (*got)[0] != want {
				t.Errorf("sent %+v, want %+v", (*got)[0], want)
			}
		})
	}
}

func TestProtocolReturn(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		err    error
	}{
		{"returned", 200, `{"id":"1","gpu":"0000:09:00.0","mib":8208}`, nil},
		{"nothing open is returned", 404, `{"error":"no open lease 1"}`, nil},
		{"still in use", 409, `{"error":"lease 1's memory is still in use","retry":true}`, ErrStillInUse},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, got := engine(t, c.status, c.body)
			a, _ := For("maya", "", srv.URL)
			err := a.Return(context.Background(), Ticket{ID: "1", GPU: "0000:09:00.0", MiB: 8208})
			if !errors.Is(err, c.err) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			want := seen{"POST", "/vram/return", "application/json", "", `{"id":"1"}`}
			if (*got)[0] != want {
				t.Errorf("sent %+v, want %+v", (*got)[0], want)
			}
		})
	}
}

func TestProtocolOpen(t *testing.T) {
	srv, got := engine(t, 200, `{"gpus":[{"gpu":"0000:09:00.0","lendable":10320,"lent":8208}],
		"leases":[{"id":"1","gpu":"0000:09:00.0","mib":8208},{"id":"2","gpu":"00000000:05:00.0","mib":512}]}`)
	a, _ := For("maya", "", srv.URL)
	ts, err := a.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Ticket{{ID: "1", GPU: "0000:09:00.0", MiB: 8208}, {ID: "2", GPU: "0000:05:00.0", MiB: 512}}
	if !reflect.DeepEqual(ts, want) {
		t.Errorf("open = %+v, want %+v", ts, want)
	}
	if g := (*got)[0]; g.Method != "GET" || g.Path != "/vram/state" || g.Body != "" {
		t.Errorf("sent %+v, want GET /vram/state with no body", g)
	}
}

func TestProtocolOtherStatusNamesTheEndpoint(t *testing.T) {
	srv, _ := engine(t, 500, "boom")
	a, _ := For("maya", "", srv.URL)
	_, err := a.Lend(context.Background(), Ask{GPU: "0000:09:00.0", MiB: 1})
	if err == nil || err.Error() != "POST "+srv.URL+"/vram/lend: 500 Internal Server Error" {
		t.Fatalf("err = %v, want the endpoint and status named", err)
	}
}

// strataLike answers POST /v1/vram as Strata does: it needs JSON, refuses
// while busy, refuses when not elastic, and reports the card's free memory
// as the reserve it reached.
func strataLike(t *testing.T, busy, elastic *bool, startFree uint64) (*httptest.Server, *[]any) {
	t.Helper()
	asked := []any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/vram" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, `{"error":{"message":"send application/json"}}`, http.StatusUnsupportedMediaType)
			return
		}
		var req struct {
			Reserve *uint64 `json:"reserve_mib"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "not JSON", http.StatusBadRequest)
			return
		}
		switch {
		case *busy:
			http.Error(w, `{"error":{"type":"model_busy"}}`, http.StatusConflict)
			return
		case !*elastic:
			http.Error(w, `{"error":{"message":"started without --vram-elastic"}}`, http.StatusBadRequest)
			return
		}
		free := startFree
		if req.Reserve != nil {
			asked = append(asked, *req.Reserve)
			free = *req.Reserve + 16
		} else {
			asked = append(asked, nil)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "vram_free_mib": free, "expert_slots": 40})
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

func TestStrataLendAndReturn(t *testing.T) {
	busy, elastic := false, true
	srv, asked := strataLike(t, &busy, &elastic, 840)
	a, ok := For("strata", srv.URL, "")
	if !ok {
		t.Fatal("strata lends with no lend_url")
	}
	tk, err := a.Lend(context.Background(), Ask{GPU: "00000000:01:00.0", MiB: 4096, FreeMiB: 840})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Ticket{ID: "reserve", GPU: "0000:01:00.0", MiB: 4112}); tk != want {
		t.Errorf("ticket = %+v, want %+v", tk, want)
	}
	if err := a.Return(context.Background(), tk); err != nil {
		t.Fatal(err)
	}
	if want := []any{uint64(4936), nil}; !reflect.DeepEqual(*asked, want) {
		t.Errorf("reserves asked = %v, want %v", *asked, want)
	}
	if ts, err := a.Open(context.Background()); err != nil || ts != nil {
		t.Errorf("open = %v, %v; want nothing, Strata keeps no ledger", ts, err)
	}
}

func TestStrataRefusals(t *testing.T) {
	busy, elastic := true, true
	srv, _ := strataLike(t, &busy, &elastic, 840)
	a, _ := For("strata", srv.URL, "")
	if _, err := a.Lend(context.Background(), Ask{MiB: 1}); !errors.Is(err, ErrBusy) {
		t.Errorf("busy lend = %v, want ErrBusy", err)
	}
	if err := a.Return(context.Background(), Ticket{ID: "reserve"}); !errors.Is(err, ErrStillInUse) {
		t.Errorf("busy return = %v, want ErrStillInUse", err)
	}
	busy, elastic = false, false
	if _, err := a.Lend(context.Background(), Ask{MiB: 1}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("lend without --vram-elastic = %v, want ErrUnsupported", err)
	}
}

func TestForNeedsAnAdapter(t *testing.T) {
	if _, ok := For("llamacpp", "http://127.0.0.1:8080", ""); ok {
		t.Error("a llamacpp engine with no lend_url has no lend adapter")
	}
	if _, ok := For("strata", "", ""); ok {
		t.Error("strata with no url has nowhere to call")
	}
}
