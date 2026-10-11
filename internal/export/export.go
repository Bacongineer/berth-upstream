// Package export dumps the harbor's stored history for the operator's own
// analysis: the requests table and the history samples, as CSV (wide and
// flattened) or JSON (the store's wire shape verbatim). The CLI reads the
// store through its read methods only; the writers here re-shape nothing
// beyond the documented flattening.
package export

import (
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/T-Crypt/berth/internal/proto"
)

// requestsHeader is the fixed column order of the requests CSV.
var requestsHeader = []string{
	"t", "id", "engine", "model", "status",
	"prompt_tokens", "cache_tokens", "output_tokens", "draft_tokens", "draft_accepted",
	"prefill_tps", "decode_tps", "duration_ms", "prefill_ms",
}

// gpuFields is the per-GPU column block: one small table that both names the
// header (gpu<N> + suffix) and fills the row's cells for index N.
var gpuFields = []struct {
	suffix string
	get    func(g proto.GPUSample) string
}{
	{"_used_mib", func(g proto.GPUSample) string { return formatMiB(g.Used) }},
	{"_util_pct", func(g proto.GPUSample) string { return formatFloat(g.Util) }},
	{"_temp_c", func(g proto.GPUSample) string { return formatFloat(g.Temp) }},
	{"_power_w", func(g proto.GPUSample) string { return formatFloat(g.Power) }},
}

// engineFields is the per-engine column block, same idea: prefix + engine
// name + suffix names the header, get fills the cell. A sample without the
// series writes "".
var engineFields = []struct {
	prefix string
	suffix string
	get    func(s proto.Sample, e string) string
}{
	{"holder_", "_mib", func(s proto.Sample, e string) string {
		b, ok := s.Holders[e]
		if !ok {
			return ""
		}
		return formatMiB(b)
	}},
	{"decode_", "_tps", func(s proto.Sample, e string) string {
		v, ok := s.Decode[e]
		if !ok {
			return ""
		}
		return formatFloat(v)
	}},
	{"kv_", "_pressure", func(s proto.Sample, e string) string {
		st, ok := s.KV[e]
		if !ok {
			return ""
		}
		return formatFloat(st.Pressure)
	}},
	{"kv_", "_window", func(s proto.Sample, e string) string {
		st, ok := s.KV[e]
		if !ok {
			return ""
		}
		return strconv.Itoa(st.Window)
	}},
	{"kv_", "_models", func(s proto.Sample, e string) string {
		st, ok := s.KV[e]
		if !ok || len(st.Models) == 0 {
			return ""
		}
		names := make([]string, 0, len(st.Models))
		for m := range st.Models {
			names = append(names, m)
		}
		sort.Strings(names)
		pairs := make([]string, len(names))
		for i, m := range names {
			pairs[i] = fmt.Sprintf("%s:%d", m, st.Models[m])
		}
		return joinSemi(pairs)
	}},
}

// formatFloat is the shortest round-trip decimal, never an exponent.
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// formatMiB converts driver bytes to MiB with one decimal.
func formatMiB(v uint64) string {
	return strconv.FormatFloat(float64(v)/1048576, 'f', 1, 64)
}

// draftCell keeps the wire's zero = "engine did not report it": 0 writes
// empty, so a spreadsheet reads zero as absence, not as a reported zero.
func draftCell(v float64) string {
	if v == 0 {
		return ""
	}
	return formatFloat(v)
}

// RequestsCSV writes one row per stored request, in order: the table's unix
// seconds as RFC3339 UTC, status as an integer, the token and rate fields as
// shortest decimals, and zero draft figures as empty cells.
func RequestsCSV(reqs []proto.Request, w io.Writer) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(requestsHeader); err != nil {
		return err
	}
	for _, r := range reqs {
		row := []string{
			r.T.UTC().Format(time.RFC3339),
			r.ID,
			r.Engine,
			r.Model,
			strconv.FormatInt(int64(r.Status), 10),
			formatFloat(r.Prompt),
			formatFloat(r.Cache),
			formatFloat(r.Output),
			draftCell(r.Draft),
			draftCell(r.DraftAcc),
			formatFloat(r.PrefillTPS),
			formatFloat(r.DecodeTPS),
			formatFloat(r.DurationMS),
			formatFloat(r.PrefillMS),
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// sampleColumns is the column map of one run: the sorted union of GPU
// indices and engine names across the range. Header and rows are both
// derived from it, so a series absent from one sample is a column of
// empties, never a dropped column.
type sampleColumns struct {
	gpus    []int
	engines []string
}

// unionColumns scans the range once and builds the sorted unions.
func unionColumns(h proto.History) sampleColumns {
	cols := sampleColumns{}
	gpuSeen := map[int]bool{}
	engineSeen := map[string]bool{}
	for _, s := range h.Samples {
		for _, g := range s.GPUs {
			gpuSeen[g.Index] = true
		}
		for e := range s.Holders {
			engineSeen[e] = true
		}
		for e := range s.Decode {
			engineSeen[e] = true
		}
		for e := range s.KV {
			engineSeen[e] = true
		}
	}
	for i := range gpuSeen {
		cols.gpus = append(cols.gpus, i)
	}
	sort.Ints(cols.gpus)
	for e := range engineSeen {
		cols.engines = append(cols.engines, e)
	}
	sort.Strings(cols.engines)
	return cols
}

// samplesHeader is the header of one run's samples CSV: the fixed
// sample-level columns, then the per-GPU and per-engine blocks from the
// column map.
func samplesHeader(cols sampleColumns) []string {
	header := []string{"t", "cpu_pct", "ram_used_mib", "waiting", "squat_mib", "rpm", "p95_ms"}
	for _, i := range cols.gpus {
		for _, f := range gpuFields {
			header = append(header, fmt.Sprintf("gpu%d%s", i, f.suffix))
		}
	}
	for _, e := range cols.engines {
		for _, f := range engineFields {
			header = append(header, f.prefix+e+f.suffix)
		}
	}
	return header
}

// SamplesCSV flattens one range of history into wide rows. Bytes become
// MiB, per-engine maps become the run's sorted-union columns, and a sample
// missing a series in the union gets empty cells for it.
func SamplesCSV(h proto.History, w io.Writer) error {
	cols := unionColumns(h)
	cw := csv.NewWriter(w)
	if err := cw.Write(samplesHeader(cols)); err != nil {
		return err
	}
	for _, s := range h.Samples {
		row := []string{
			s.T.UTC().Format(time.RFC3339),
			formatFloat(s.CPU),
			formatMiB(s.RAMUsed),
			strconv.Itoa(s.Waiting),
			formatMiB(s.Squat),
			formatFloat(s.RPM),
			formatFloat(s.P95MS),
		}
		byIdx := map[int]proto.GPUSample{}
		for _, g := range s.GPUs {
			byIdx[g.Index] = g
		}
		for _, i := range cols.gpus {
			g, ok := byIdx[i]
			if !ok {
				row = append(row, make([]string, len(gpuFields))...)
				continue
			}
			for _, f := range gpuFields {
				row = append(row, f.get(g))
			}
		}
		for _, e := range cols.engines {
			for _, f := range engineFields {
				row = append(row, f.get(s, e))
			}
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// joinSemi joins with ";", the CSV-safe separator inside a cell.
func joinSemi(pairs []string) string {
	out := ""
	for i, p := range pairs {
		if i > 0 {
			out += ";"
		}
		out += p
	}
	return out
}
