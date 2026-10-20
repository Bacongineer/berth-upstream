// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"os"
	"strconv"

	"github.com/T-Crypt/berth/internal/civitai"
)

func civitaiClient(c *Context, tokenEnv string) *civitai.Client {
	token := ""
	if tokenEnv != "" {
		token = os.Getenv(tokenEnv)
	}
	return &civitai.Client{Token: token}
}

type civitaiSearchDoc struct {
	Status string          `json:"status"`
	Query  string          `json:"query"`
	Models []civitai.Model `json:"models"`
}

type civitaiSearchView struct{ doc civitaiSearchDoc }

func (v civitaiSearchView) Data() any { return v.doc }

func (v civitaiSearchView) Human(c *Context, ok bool) error {
	if len(v.doc.Models) == 0 {
		c.Printf("no models match %q", v.doc.Query)
		return nil
	}
	c.Printf("%-10s %-28s %-12s %-12s %s", "ID", "NAME", "TYPE", "BASE", "CREATOR")
	for _, m := range v.doc.Models {
		c.Printf("%-10d %-28.28s %-12s %-12s %s", m.ID, m.Name, m.Type, m.BaseModel, m.Creator)
	}
	return nil
}

func runCivitaiSearch(c *Context) int {
	var types stringList
	fs := c.FlagSet()
	fs.Var(&types, "type", "an asset kind to include (Checkpoint, Lora, ...), repeatable")
	fLimit := fs.Int("limit", 20, "how many results to ask for, at most 100")
	fToken := fs.String("token-env", "CIVITAI_API_KEY", "the environment variable holding the API key, for gated results")
	if code, done := c.ParseInterspersed(fs); done {
		return code
	}
	if len(c.Args) != 1 {
		return c.fail(Systemf("civitai search takes one query").
			WithHint("wrap it in quotes: berth civitai search \"flux lora\""))
	}
	query := c.Args[0]
	client := civitaiClient(c, *fToken)
	ms, e := client.Search(context.Background(), query, types.vals, *fLimit)
	if e != nil {
		return c.fail(Failf("%v", e))
	}
	doc := civitaiSearchDoc{Status: "ok", Query: query, Models: ms}
	if len(ms) == 0 {
		doc.Models = []civitai.Model{}
	}
	return c.Write(civitaiSearchView{doc}, nil)
}

type civitaiVersionsDoc struct {
	Status   string                   `json:"status"`
	ModelID  int64                    `json:"model_id"`
	Versions []civitai.VersionSummary `json:"versions"`
}

type civitaiVersionsView struct{ doc civitaiVersionsDoc }

func (v civitaiVersionsView) Data() any { return v.doc }

func (v civitaiVersionsView) Human(c *Context, ok bool) error {
	if len(v.doc.Versions) == 0 {
		c.Printf("model %d has no versions", v.doc.ModelID)
		return nil
	}
	c.Printf("%-10s %-24s %s", "ID", "NAME", "CREATED")
	for _, vs := range v.doc.Versions {
		c.Printf("%-10d %-24.24s %s", vs.ID, vs.Name, vs.CreatedAt)
	}
	return nil
}

func runCivitaiVersions(c *Context) int {
	fs := c.FlagSet()
	fToken := fs.String("token-env", "CIVITAI_API_KEY", "the environment variable holding the API key")
	if code, done := c.ParseInterspersed(fs); done {
		return code
	}
	if len(c.Args) != 1 {
		return c.fail(Systemf("civitai versions takes one model id").
			WithHint("find ids with: berth civitai search \"the model name\""))
	}
	id, err := strconv.ParseInt(c.Args[0], 10, 64)
	if err != nil {
		return c.fail(Failf("the model id %q is not a number", c.Args[0]).
			WithHint("model ids are digits, as printed by: berth civitai search"))
	}
	client := civitaiClient(c, *fToken)
	vs, e := client.ModelVersions(context.Background(), id)
	if e != nil {
		return c.fail(Failf("%v", e))
	}
	doc := civitaiVersionsDoc{Status: "ok", ModelID: id, Versions: vs}
	if len(vs) == 0 {
		doc.Versions = []civitai.VersionSummary{}
	}
	return c.Write(civitaiVersionsView{doc}, nil)
}
