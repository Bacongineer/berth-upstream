// Package civitai reads model versions from the CivitAI site and moves
// their files into a root, checked against the hash the site publishes,
// with the same part-file resume as a Hub pull.
package civitai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/T-Crypt/berth/internal/dl"
	"github.com/T-Crypt/berth/internal/manifest"
)

// Endpoint is the site; CIVITAI_ENDPOINT overrides it (mirrors, tests).
func Endpoint() string {
	if e := os.Getenv("CIVITAI_ENDPOINT"); e != "" {
		return strings.TrimRight(e, "/")
	}
	return "https://civitai.com"
}

// Ref is what to pull: a version id, or a model id whose latest version is
// pulled instead.
type Ref struct {
	VersionID string
	ModelID   string
}

// ParseRef reads civitai:<version-id>, civitai:model:<model-id>, a bare id,
// and the site's own URLs.
func ParseRef(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "civitai:")
	for _, prefix := range []string{"https://civitai.com/model-versions/", "https://civitai.com/models/"} {
		if strings.HasPrefix(s, prefix) {
			rest := strings.TrimSuffix(strings.TrimPrefix(s, prefix), "/")
			if id, err := strconv.ParseInt(rest, 10, 64); err == nil && id > 0 {
				if strings.Contains(prefix, "model-versions") {
					return Ref{VersionID: rest}, nil
				}
				return Ref{ModelID: strconv.FormatInt(id, 10)}, nil
			}
		}
	}
	if strings.HasPrefix(s, "model:") {
		id := strings.TrimPrefix(s, "model:")
		if _, err := strconv.ParseInt(id, 10, 64); err != nil || id == "" {
			return Ref{}, fmt.Errorf("civitai:model:%s: the model id is a number", id)
		}
		return Ref{ModelID: id}, nil
	}
	if _, err := strconv.ParseInt(s, 10, 64); err != nil || s == "" {
		return Ref{}, fmt.Errorf("civitai:%s: name a version id, a model id as civitai:model:<id>, or the site's URL", s)
	}
	return Ref{VersionID: s}, nil
}

// Client talks to the site. Token is the API key, for gated files; public
// search and pulls go without one.
type Client struct {
	HTTP  *http.Client
	Token string
	Base  string // "" = Endpoint()
}

func (c *Client) base() string {
	if c.Base != "" {
		return strings.TrimRight(c.Base, "/")
	}
	return Endpoint()
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 0}
}

func (c *Client) get(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("User-Agent", "berth")
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("the site answered %s: gated; set its API key", resp.Status)
	case http.StatusNotFound:
		return fmt.Errorf("the site answered %s: no such model or version", resp.Status)
	default:
		return fmt.Errorf("the site answered %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Model is one search result.
type Model struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Type      string  `json:"type"`
	BaseModel string  `json:"baseModel,omitempty"`
	Creator   string  `json:"creator,omitempty"`
	Downloads int64   `json:"downloads,omitempty"`
	Rating    float64 `json:"rating,omitempty"`
}

// Search asks the site's model search. Types filters by asset kind
// (Checkpoint, Lora, ...); an empty list is every kind.
func (c *Client) Search(ctx context.Context, query string, types []string, limit int) ([]Model, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	u := fmt.Sprintf("%s/api/v1/models?query=%s&limit=%d", c.base(), url.QueryEscape(query), limit)
	for _, t := range types {
		u += "&types[]=" + url.QueryEscape(t)
	}
	var r struct {
		Items []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			Type string `json:"type"`
			User *struct {
				Name string `json:"name"`
			} `json:"user"`
			Metadata *struct {
				BaseModel string `json:"baseModel"`
			} `json:"metadata"`
			Stats *struct {
				DownloadCount int64   `json:"downloadCount"`
				Rating        float64 `json:"rating"`
			} `json:"stats"`
		} `json:"items"`
	}
	if err := c.get(ctx, u, &r); err != nil {
		return nil, err
	}
	var out []Model
	for _, it := range r.Items {
		m := Model{ID: it.ID, Name: it.Name, Type: it.Type}
		if it.User != nil {
			m.Creator = it.User.Name
		}
		if it.Metadata != nil {
			m.BaseModel = it.Metadata.BaseModel
		}
		if it.Stats != nil {
			m.Downloads, m.Rating = it.Stats.DownloadCount, it.Stats.Rating
		}
		out = append(out, m)
	}
	return out, nil
}

// VersionSummary is one version of a model.
type VersionSummary struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// ModelVersions lists a model's versions, newest first, as the site orders
// them.
func (c *Client) ModelVersions(ctx context.Context, modelID int64) ([]VersionSummary, error) {
	var r struct {
		Items []VersionSummary `json:"items"`
	}
	if err := c.get(ctx, fmt.Sprintf("%s/api/v1/models/%d/versions?limit=50", c.base(), modelID), &r); err != nil {
		return nil, err
	}
	return r.Items, nil
}

// File is one downloadable file of a version.
type File struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	SHA256   string `json:"hash"`
	Type     string `json:"type,omitempty"`
	Download string `json:"downloadUrl"`
}

// Version is a model version and what it holds.
type Version struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	ModelID      int64  `json:"modelId"`
	ModelName    string `json:"modelName"`
	Type         string `json:"type"`
	BaseModel    string `json:"baseModel,omitempty"`
	TriggerWords string `json:"triggerWords,omitempty"`
	Files        []File `json:"files"`
}

// Version reads one model version in full, including its files and their
// download links.
func (c *Client) Version(ctx context.Context, versionID int64) (Version, error) {
	var raw struct {
		ID           int64  `json:"id"`
		Name         string `json:"name"`
		Type         string `json:"type"`
		TriggerWords string `json:"triggerWords"`
		Model        *struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			Metadata *struct {
				BaseModel string `json:"baseModel"`
			} `json:"metadata"`
		} `json:"model"`
		Files []File `json:"files"`
	}
	if err := c.get(ctx, fmt.Sprintf("%s/api/v1/model-versions/%d", c.base(), versionID), &raw); err != nil {
		return Version{}, err
	}
	v := Version{ID: raw.ID, Name: raw.Name, Type: raw.Type, TriggerWords: raw.TriggerWords, Files: raw.Files}
	if raw.Model != nil {
		v.ModelID, v.ModelName = raw.Model.ID, raw.Model.Name
		if raw.Model.Metadata != nil {
			v.BaseModel = raw.Model.Metadata.BaseModel
		}
	}
	return v, nil
}

// LatestVersion is a model's first version in the site's order.
func (c *Client) LatestVersion(ctx context.Context, modelID int64) (VersionSummary, error) {
	vs, err := c.ModelVersions(ctx, modelID)
	if err != nil {
		return VersionSummary{}, err
	}
	if len(vs) == 0 {
		return VersionSummary{}, fmt.Errorf("model %d has no versions", modelID)
	}
	return vs[0], nil
}

// Download moves one of a version's files into destDir, under the file's
// own name, with part-file resume and the site's hash checked before the
// file is accepted.
func (c *Client) Download(ctx context.Context, v Version, f File, destDir string, prog func(done, total int64)) (skipped bool, err error) {
	name := filepath.Base(strings.ReplaceAll(f.Name, "\\", "/"))
	if name == "" || name == "." {
		name = "model"
	}
	dst := filepath.Join(destDir, name)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	if st, err := os.Stat(dst); err == nil && st.Size() == f.Size {
		if f.SHA256 == "" {
			return true, nil
		}
		if sum, err := manifest.HashFile(dst, nil); err == nil && sum == f.SHA256 {
			return true, nil
		}
	}
	spec := dl.FetchSpec{
		URL:    f.Download,
		Part:   dst + ".part",
		Dest:   dst,
		Size:   f.Size,
		SHA256: f.SHA256,
		Prog:   prog,
	}
	if c.Token != "" {
		spec.Header = map[string]string{"Authorization": "Bearer " + c.Token}
	}
	spec.Label = "CivitAI"
	if err := dl.Retry(ctx, c.http(), spec, 5); err != nil {
		return false, err
	}
	return false, nil
}

// slug is a directory name for a version: its model name, lowered and
// squashed to safe characters, runs of separators folded to one dash.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == ' ' || r == '-' || r == '_' || r == '.':
			if !dash && b.Len() > 0 {
				b.WriteRune('-')
				dash = true
			}
		}
	}
	out := strings.TrimSuffix(b.String(), "-")
	if out == "" {
		return "model"
	}
	return out
}

// ResolveRef finds the version a ref names: the version itself, or a model's
// latest version.
func (c *Client) ResolveRef(ctx context.Context, ref Ref) (Version, error) {
	if ref.VersionID != "" {
		id, err := strconv.ParseInt(ref.VersionID, 10, 64)
		if err != nil {
			return Version{}, fmt.Errorf("version id %q is not a number", ref.VersionID)
		}
		return c.Version(ctx, id)
	}
	id, err := strconv.ParseInt(ref.ModelID, 10, 64)
	if err != nil {
		return Version{}, fmt.Errorf("model id %q is not a number", ref.ModelID)
	}
	vs, err := c.LatestVersion(ctx, id)
	if err != nil {
		return Version{}, err
	}
	return c.Version(ctx, vs.ID)
}
