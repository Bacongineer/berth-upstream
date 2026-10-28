// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/node"
	"github.com/T-Crypt/berth/internal/proto"
)

// ServeMetrics serves Prometheus text on a loopback address only. It is
// read-only, and requests whose Host header is not a loopback name are
// refused, so a web page cannot read it through DNS rebinding.
func ServeMetrics(ctx context.Context, addr string, n *node.Node, engines func() []proto.Engine, log *slog.Logger) error {
	if err := config.Loopback(addr); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		st := n.Status()
		st.Engines = engines()
		st.Counters.SendTimeouts = sendTimeouts.Load()
		st.Counters.DroppedConsumers = droppedConsumers.Load()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		WriteProm(w, st)
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	log.Info("metrics up", "addr", "http://"+addr+"/metrics")
	if err := srv.Serve(ln); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func loopbackHost(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

var promEscaper = strings.NewReplacer("\\", `\\`, `"`, `\"`, "\n", `\n`)

// promEsc escapes a label value for the Prometheus text format.
func promEsc(v string) string { return promEscaper.Replace(v) }

// sortedCards returns the scheduled cards, then any other card a lease,
// waiting entry or squatter names, each once and in name order.
func sortedCards(cards []string, st proto.Status) []string {
	seen := map[string]bool{}
	var out []string
	add := func(c string) {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	for _, c := range cards {
		add(c)
	}
	for _, l := range st.Leases {
		add(l.Card)
	}
	for _, l := range st.Waiting {
		add(l.Card)
	}
	for _, s := range st.Squatters {
		add(s.Card)
	}
	sort.Strings(out)
	return out
}

func isCard(cards []string, c string) bool {
	for _, x := range cards {
		if x == c {
			return true
		}
	}
	return false
}

// WriteProm renders status as Prometheus text. Names are berth_*; memory
// is bytes; engine figures carry source="engine" because they are the
// engine's own claims, not driver measurements.
func WriteProm(w io.Writer, st proto.Status) {
	p := func(help, typ, name string, series ...string) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
		for _, s := range series {
			fmt.Fprintln(w, s)
		}
	}
	var total, used, free, base, util, temp, pow, plim, fan []string
	var cards []string
	for _, g := range st.GPUs {
		if g.Scheduled {
			cards = append(cards, g.Card)
		}
		l := fmt.Sprintf(`{gpu="%d",card="%s",name="%s",scheduled="%t"}`, g.Index, promEsc(g.Card), promEsc(g.Name), g.Scheduled)
		total = append(total, fmt.Sprintf("berth_gpu_memory_total_bytes%s %d", l, g.Total))
		used = append(used, fmt.Sprintf("berth_gpu_memory_used_bytes%s %d", l, g.Used))
		free = append(free, fmt.Sprintf("berth_gpu_memory_free_bytes%s %d", l, g.Free))
		l = fmt.Sprintf(`{gpu="%d",card="%s"}`, g.Index, promEsc(g.Card))
		base = append(base, fmt.Sprintf("berth_gpu_baseline_bytes%s %d", l, g.Baseline))
		if g.Util >= 0 {
			util = append(util, fmt.Sprintf("berth_gpu_utilization_ratio%s %g", l, g.Util/100))
		}
		if g.Temp >= 0 {
			temp = append(temp, fmt.Sprintf("berth_gpu_temperature_celsius%s %g", l, g.Temp))
		}
		if g.Power >= 0 {
			pow = append(pow, fmt.Sprintf("berth_gpu_power_watts%s %g", l, g.Power))
		}
		if g.PowerLimit >= 0 {
			plim = append(plim, fmt.Sprintf("berth_gpu_power_limit_watts%s %g", l, g.PowerLimit))
		}
		if g.Fan >= 0 {
			fan = append(fan, fmt.Sprintf("berth_gpu_fan_ratio%s %g", l, g.Fan/100))
		}
	}
	p("Card memory, measured by the driver.", "gauge", "berth_gpu_memory_total_bytes", total...)
	p("Card memory in use, measured by the driver.", "gauge", "berth_gpu_memory_used_bytes", used...)
	p("Card memory free, measured by the driver.", "gauge", "berth_gpu_memory_free_bytes", free...)
	p("Card memory held by processes outside any lease at the last baseline.", "gauge", "berth_gpu_baseline_bytes", base...)
	// Telemetry the driver does not report is -1, and gets no series.
	if len(util) > 0 {
		p("Card utilization as a ratio from 0 to 1.", "gauge", "berth_gpu_utilization_ratio", util...)
	}
	if len(temp) > 0 {
		p("Card temperature in degrees Celsius.", "gauge", "berth_gpu_temperature_celsius", temp...)
	}
	if len(pow) > 0 {
		p("Card power draw in watts.", "gauge", "berth_gpu_power_watts", pow...)
	}
	if len(plim) > 0 {
		p("Card power limit in watts.", "gauge", "berth_gpu_power_limit_watts", plim...)
	}
	if len(fan) > 0 {
		p("Card fan speed as a ratio from 0 to 1.", "gauge", "berth_gpu_fan_ratio", fan...)
	}

	// Every scheduled card gets a zero series, so a graph shows an empty
	// card as 0 and not as a gap. A lease on another card still counts.
	leaseStates := []string{"active", "draining", "parking", "parked", "releasing", "stuck"}
	type key struct{ card, state string }
	count := map[key]int{}
	for _, c := range cards {
		for _, s := range leaseStates {
			count[key{c, s}] = 0
		}
	}
	type lkey struct{ card, engine, priority, state string }
	leaseBytes := map[lkey]uint64{}
	for _, l := range st.Leases {
		count[key{l.Card, l.State}]++
		leaseBytes[lkey{l.Card, l.Engine, l.Priority, l.State}] += l.Used
	}
	var ls, lb []string
	for _, c := range sortedCards(cards, st) {
		for _, s := range leaseStates {
			if n, ok := count[key{c, s}]; ok {
				ls = append(ls, fmt.Sprintf(`berth_leases{card="%s",state="%s"} %d`, promEsc(c), s, n))
			}
		}
	}
	p("Leases by card and state.", "gauge", "berth_leases", ls...)
	lks := make([]lkey, 0, len(leaseBytes))
	for k := range leaseBytes {
		lks = append(lks, k)
	}
	slices.SortFunc(lks, func(a, b lkey) int {
		return cmp.Or(cmp.Compare(a.card, b.card), cmp.Compare(a.engine, b.engine),
			cmp.Compare(a.priority, b.priority), cmp.Compare(a.state, b.state))
	})
	for _, k := range lks {
		lb = append(lb, fmt.Sprintf(`berth_lease_used_bytes{card="%s",engine="%s",priority="%s",state="%s"} %d`,
			promEsc(k.card), promEsc(k.engine), promEsc(k.priority), promEsc(k.state), leaseBytes[k]))
	}
	if len(lb) > 0 {
		p("GPU memory the leases hold, measured by the driver, summed per label set.", "gauge", "berth_lease_used_bytes", lb...)
	}

	waiting := map[string]int{}
	for _, w := range st.Waiting {
		waiting[w.Card]++
	}
	squats := map[string]int{}
	squatBytes := map[string]uint64{}
	for _, s := range st.Squatters {
		squats[s.Card]++
		squatBytes[s.Card] += s.Used
	}
	var wt, sq, sqb []string
	for _, c := range sortedCards(cards, st) {
		_, inW := waiting[c]
		_, inS := squats[c]
		if !inW && !inS && !isCard(cards, c) {
			continue
		}
		l := fmt.Sprintf(`{card="%s"}`, promEsc(c))
		wt = append(wt, fmt.Sprintf("berth_waiting%s %d", l, waiting[c]))
		sq = append(sq, fmt.Sprintf("berth_squatters%s %d", l, squats[c]))
		sqb = append(sqb, fmt.Sprintf("berth_squatter_bytes%s %d", l, squatBytes[c]))
	}
	p("Requests waiting for a berth.", "gauge", "berth_waiting", wt...)
	p("Processes holding GPU memory without a lease.", "gauge", "berth_squatters", sq...)
	p("GPU memory held without a lease.", "gauge", "berth_squatter_bytes", sqb...)
	p("Engines running without a lease.", "gauge", "berth_unprotected_runs", fmt.Sprintf("berth_unprotected_runs %d", len(st.Unprotected)))
	probe := 1
	if st.ProbeErr != "" {
		probe = 0
	}
	p("1 when the GPU probe is reading.", "gauge", "berth_probe_up", fmt.Sprintf("berth_probe_up %d", probe))

	p("Berth version.", "gauge", "berth_build_info", fmt.Sprintf(`berth_build_info{version="%s"} 1`, promEsc(st.Version)))

	c := st.Counters
	p("Grants that waited on a held card instead of colliding.", "counter", "berth_collisions_avoided_total", fmt.Sprintf("berth_collisions_avoided_total %d", c.CollisionsAvoided))
	p("Peak measured bytes of leases whose memory was freed.", "counter", "berth_vram_handed_off_bytes_total", fmt.Sprintf("berth_vram_handed_off_bytes_total %d", c.VRAMHandedOff))
	p("Drain requests sent.", "counter", "berth_drains_total", fmt.Sprintf("berth_drains_total %d", c.Drains))
	p("Released leases whose memory did not free in time.", "counter", "berth_stuck_total", fmt.Sprintf("berth_stuck_total %d", c.StuckCaught))
	p("Squatters seen.", "counter", "berth_squatters_caught_total", fmt.Sprintf("berth_squatters_caught_total %d", c.SquattersCaught))
	p("Grants made while unleased processes held memory.", "counter", "berth_contested_grants_total", fmt.Sprintf("berth_contested_grants_total %d", c.ContestedGrants))
	p("Requests refused.", "counter", "berth_refused_total", fmt.Sprintf("berth_refused_total %d", c.Refused))
	p("Equal-priority handoffs the engine ranking decided instead of flipping the card.", "counter", "berth_juggles_stopped_total", fmt.Sprintf("berth_juggles_stopped_total %d", c.JugglesStopped))
	p("Fan-out writes that hit the per-message deadline.", "counter", "berth_send_timeouts_total", fmt.Sprintf("berth_send_timeouts_total %d", c.SendTimeouts))
	p("Socket consumers dropped for slow reads.", "counter", "berth_dropped_consumers_total", fmt.Sprintf("berth_dropped_consumers_total %d", c.DroppedConsumers))

	var up, models, dec, pre, out, in []string
	for _, e := range st.Engines {
		l := fmt.Sprintf(`{engine="%s",type="%s",source="engine"}`, promEsc(e.Name), promEsc(e.Type))
		b := 0
		if e.Up {
			b = 1
		}
		up = append(up, fmt.Sprintf("berth_engine_up%s %d", l, b))
		models = append(models, fmt.Sprintf("berth_engine_models%s %d", l, len(e.Models)))
		dec = append(dec, fmt.Sprintf("berth_engine_decode_tokens_per_second%s %g", l, e.DecodeTPS))
		pre = append(pre, fmt.Sprintf("berth_engine_prefill_tokens_per_second%s %g", l, e.PrefillTPS))
		out = append(out, fmt.Sprintf("berth_engine_output_tokens_total%s %g", l, e.Metrics.OutputTokens))
		in = append(in, fmt.Sprintf("berth_engine_prompt_tokens_total%s %g", l, e.Metrics.PromptTokens))
	}
	if len(st.Engines) > 0 {
		p("1 when the engine answered its last read.", "gauge", "berth_engine_up", up...)
		p("Models the engine reports loaded.", "gauge", "berth_engine_models", models...)
		p("Decode rate over the last read interval, from the engine's counters.", "gauge", "berth_engine_decode_tokens_per_second", dec...)
		p("Prefill rate over the last read interval, from the engine's counters.", "gauge", "berth_engine_prefill_tokens_per_second", pre...)
		p("Output tokens the engine reports since it started.", "counter", "berth_engine_output_tokens_total", out...)
		p("Prompt tokens the engine reports since it started.", "counter", "berth_engine_prompt_tokens_total", in...)
	}
}
