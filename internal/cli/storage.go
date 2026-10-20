package cli

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/models"
	"github.com/T-Crypt/berth/internal/storage"
)

// floorGuard measures the destination root and refuses the pull when it
// would leave the root under its floor. A root whose space cannot be
// measured warns instead: a guard that cannot measure must not pretend it
// guards, but it must say so.
func floorGuard(c *Context, dest string, need uint64, floor float64) *Error {
	root, err := storage.Info(dest)
	if err != nil {
		// The destination may not exist yet: walk up to the root that does.
		p := dest
		for {
			up := filepath.Dir(p)
			root, err = storage.Info(p)
			if err == nil || up == p {
				break
			}
			p = up
		}
		if err != nil {
			return nil // no measurable root anywhere: nothing to guard against
		}
	}
	under, err := root.WouldGoUnderFloor(need, floor)
	if err != nil {
		if !c.Machine() {
			c.Warnf("%s sits on %s, whose free space berth cannot measure here; the floor was not checked", root.Path, root.FS)
		}
		return nil
	}
	if !under {
		return nil
	}
	return Failf("the pull would leave %s under its floor: %s free of %s, the floor keeps %s free (%.0f%%)",
		root.Path, size(root.Free), size(root.Total), size(root.FloorBytes(floor)), floor*100).
		WithHint("free space in the root, raise [library] free_floor, or lower it only if you know the drive can take it")
}

type shelfUsage struct {
	Bytes  uint64 `json:"bytes"`
	Models int    `json:"models"`
}

type catalogDoc struct {
	Host string `json:"host"`
	Age  string `json:"age"`
}

type storageRootDoc struct {
	Path       string                 `json:"path"`
	FS         string                 `json:"fs"`
	Total      uint64                 `json:"total"`
	Free       uint64                 `json:"free"`
	Used       uint64                 `json:"used"`
	Shared     string                 `json:"shared,omitempty"`
	UnderFloor bool                   `json:"under_floor"`
	Support    storage.Support        `json:"supports"`
	Shelves    map[string]*shelfUsage `json:"shelves"`
	Catalog    *catalogDoc            `json:"catalog,omitempty"`
	Models     int                    `json:"models"`
	Bytes      uint64                 `json:"bytes"`
}

type storageDoc struct {
	Status string           `json:"status"` // ok | failed
	Floor  float64          `json:"floor"`
	Roots  []storageRootDoc `json:"roots"`
}

type storageView struct{ doc storageDoc }

func (v storageView) Data() any { return v.doc }

func (v storageView) Human(c *Context, ok bool) error {
	d := v.doc
	if ok {
		c.Printf("%s %d root%s, floor %.0f%%\n", c.Style.OK(markOK), len(d.Roots), plural(len(d.Roots)), d.Floor*100)
	} else {
		c.Printf("%s %d root%s under the floor\n", c.Style.Err(markError), countUnder(d), plural(countUnder(d)))
	}
	for _, r := range d.Roots {
		head := fmt.Sprintf("%s  %s  %s free of %s (%.0f%%)",
			r.Path, c.Style.Dim(r.FS), size(r.Free), size(r.Total), pct(r.Free, r.Total))
		if r.Shared != "" {
			head += c.Style.Dim(fmt.Sprintf("  shared with %s", r.Shared))
		}
		c.Printf("%s", head)
		if r.UnderFloor {
			c.Sub("%s", c.Style.Err(fmt.Sprintf("under floor: free %s is under the %.0f%% floor", size(r.Free), d.Floor*100)))
		} else {
			c.Sub("%s", c.Style.OK("floor ok"))
		}
		order := make([]string, 0, len(r.Shelves))
		for s := range r.Shelves {
			order = append(order, s)
		}
		sort.Strings(order)
		if len(order) > 0 {
			line := ""
			for _, s := range order {
				u := r.Shelves[s]
				label := s
				if label == "" {
					label = "unshelved"
				}
				if line != "" {
					line += "  "
				}
				line += fmt.Sprintf("%s %s in %d model%s", label, size(u.Bytes), u.Models, plural(u.Models))
			}
			c.Sub("%s", line)
		}
		if r.Catalog != nil {
			c.Sub("catalog: %s, %s", r.Catalog.Host, r.Catalog.Age)
		} else {
			c.Sub("%s", c.Style.Dim("catalog: none; the root was never scanned by a berth"))
		}
	}
	return nil
}

func countUnder(d storageDoc) int {
	n := 0
	for _, r := range d.Roots {
		if r.UnderFloor {
			n++
		}
	}
	return n
}

func pct(free, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(free) / float64(total)
}

func runStorage(c *Context) int {
	var dirs stringList
	fs := c.FlagSet()
	fs.Var(&dirs, "dir", "a model root to check, repeatable (default: the [models] dirs, the [library] roots and the usual stores)")
	if code, done := c.Parse(fs); done {
		return code
	}
	if code, done := c.noArgs(); done {
		return code
	}
	cfg, _, _, e := loadConfig(c, config.DefaultPath())
	if e != nil {
		return c.fail(e)
	}
	roots := dirs.vals
	if len(roots) == 0 {
		seen := map[string]bool{}
		for _, d := range cfg.Models.Dirs {
			if !seen[d] {
				roots = append(roots, d)
				seen[d] = true
			}
		}
		for _, r := range cfg.Library.Root {
			if !seen[r.Path] {
				roots = append(roots, r.Path)
				seen[r.Path] = true
			}
		}
	}
	missing := models.MissingRoots(roots)
	if len(roots) > 0 && len(missing) == len(roots) {
		return c.fail(Systemf("no such model root: %s", strings.Join(missing, ", ")).
			WithHint("name existing roots with --dir, or run \"berth storage\" with a config that names them"))
	}
	if !c.Machine() {
		for _, d := range missing {
			c.Warnf("%s is not a directory: nothing was read there", d)
		}
	}
	scanRoots := roots
	if len(scanRoots) == 0 {
		scanRoots = models.DefaultRoots()
	}
	ms := models.Scan(scanRoots)
	// Declared roots beat what a file states.
	var declared []models.RootShelf
	for _, r := range cfg.Library.Root {
		if r.Shelf != "" {
			declared = append(declared, models.RootShelf{Path: r.Path, Shelf: models.Shelf(r.Shelf)})
		}
	}
	models.ApplyRootShelves(ms, declared)
	floor := cfg.LibraryFloor()

	doc := storageDoc{Status: "ok", Floor: floor}
	for _, p := range scanRoots {
		r, err := storage.Info(p)
		if err != nil {
			continue
		}
		rd := storageRootDoc{Path: r.Path, FS: r.FS, Total: r.Total, Free: r.Free, Used: r.Used,
			Support: r.Support, Shelves: map[string]*shelfUsage{}}
		for _, rr := range cfg.Library.Root {
			if rr.Path == p {
				rd.Shared = rr.Shared
				break
			}
		}
		if r.Detectable() {
			if under, err := r.WouldGoUnderFloor(0, floor); err == nil {
				rd.UnderFloor = under
			}
		}
		for _, m := range ms {
			if !underPath(m.Path, p) {
				continue
			}
			u := rd.Shelves[string(m.Shelf)]
			if u == nil {
				u = &shelfUsage{}
				rd.Shelves[string(m.Shelf)] = u
			}
			u.Bytes += m.Bytes
			u.Models++
			rd.Models++
			rd.Bytes += m.Bytes
		}
		if cat, err := models.ReadCatalog(p); err == nil {
			age := "never"
			if !cat.At.IsZero() {
				age = ago(cat.At)
			}
			rd.Catalog = &catalogDoc{Host: cat.Host, Age: age}
		}
		doc.Roots = append(doc.Roots, rd)
	}
	n := 0
	for _, r := range doc.Roots {
		if r.UnderFloor {
			n++
		}
	}
	var failure *Error
	if n > 0 {
		doc.Status = "failed"
		failure = Failf("%d of %d roots are under their floor", n, len(doc.Roots))
		for _, r := range doc.Roots {
			if r.UnderFloor {
				failure.Detail("%s: %s free of %s", r.Path, size(r.Free), size(r.Total))
			}
		}
	}
	return c.Write(storageView{doc: doc}, failure)
}

func underPath(p, root string) bool {
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}
