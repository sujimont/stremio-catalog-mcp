package stremio

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/amxv/stremio-catalog-mcp/pkg/catalog"
)

const AddonID = "xyz.ashray.stremio-catalog-mcp"

type Manifest struct {
	ID          string    `json:"id"`
	Version     string    `json:"version"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Resources   []string  `json:"resources"`
	Types       []string  `json:"types"`
	Catalogs    []Catalog `json:"catalogs"`
}

type Catalog struct {
	Type  string  `json:"type"`
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Extra []Extra `json:"extra,omitempty"`
}

type Extra struct {
	Name       string `json:"name"`
	IsRequired bool   `json:"isRequired"`
}

func BuildManifest(state catalog.State) Manifest {
	manifest := Manifest{
		ID: AddonID, Version: fmt.Sprintf("0.1.%d", state.Revision), Name: "Stremio Catalog MCP",
		Description: "Private catalog rows curated by your MCP agent. Streams and metadata stay with your existing addons.",
		Resources:   []string{"catalog"}, Types: []string{"movie", "series"}, Catalogs: []Catalog{},
	}
	for _, row := range state.Rows {
		if row.Hidden {
			continue
		}
		manifest.Catalogs = append(manifest.Catalogs, Catalog{Type: row.Type, ID: row.ID, Name: row.Name})
	}
	return manifest
}

func NormalizeProfile(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	var clean strings.Builder
	for _, r := range p {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			clean.WriteRune(r)
		}
	}
	return clean.String()
}

func ProfileManifestURL(baseURL, feedToken, profile string, revisions ...int64) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || feedToken == "" || strings.ContainsAny(feedToken, "/?# ") {
		return "", errors.New("PUBLIC_BASE_URL must be https and STREMIO_FEED_TOKEN must be a URL-safe secret")
	}
	version := ""
	if len(revisions) != 0 {
		version = fmt.Sprintf("/v%d", revisions[0])
	}
	profile = NormalizeProfile(profile)
	if profile == "" || profile == "default" {
		return strings.TrimRight(u.String(), "/") + "/stremio/" + feedToken + version + "/manifest.json", nil
	}
	return strings.TrimRight(u.String(), "/") + "/stremio/" + feedToken + "/p/" + url.PathEscape(profile) + version + "/manifest.json", nil
}

func ManifestURL(baseURL, feedToken string, revisions ...int64) (string, error) {
	return ProfileManifestURL(baseURL, feedToken, "", revisions...)
}

func EqualToken(want, got string) bool {
	if want == "" || got == "" {
		return false
	}
	a, b := sha256.Sum256([]byte(want)), sha256.Sum256([]byte(got))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

type Handler struct {
	Manager           catalog.Manager
	ManagerForProfile func(profile string) catalog.Manager
	FeedToken         string
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Vercel rewrites map /stremio/... to /api/mcp with explicit parameters.
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var token, resource, mediaType, id, profile string
	if len(parts) >= 3 && parts[0] == "stremio" {
		token = parts[1]
		idx := 2
		if len(parts) >= 4 && parts[2] == "p" {
			profile = parts[3]
			idx = 4
		}
		remaining := parts[idx:]
		if len(remaining) >= 1 && strings.HasPrefix(remaining[0], "v") {
			remaining = remaining[1:]
		}
		if len(remaining) == 1 && remaining[0] == "manifest.json" {
			resource = "manifest"
		} else if len(remaining) == 3 && remaining[0] == "catalog" && strings.HasSuffix(remaining[2], ".json") {
			resource, mediaType, id = "catalog", remaining[1], strings.TrimSuffix(remaining[2], ".json")
		}
	} else if len(parts) == 2 && parts[0] == "api" && (parts[1] == "stremio" || parts[1] == "mcp") {
		q := r.URL.Query()
		token, resource, mediaType, id, profile = q.Get("token"), q.Get("resource"), q.Get("type"), q.Get("id"), q.Get("profile")
	}
	if !EqualToken(h.FeedToken, token) {
		http.NotFound(w, r)
		return
	}
	mgr := h.Manager
	if profile != "" && h.ManagerForProfile != nil {
		mgr = h.ManagerForProfile(profile)
	}
	state, err := mgr.Read(r.Context())
	if err != nil {
		http.Error(w, "Catalog storage unavailable", http.StatusServiceUnavailable)
		return
	}
	var response any
	switch resource {
	case "manifest":
		response = BuildManifest(state)
		w.Header().Set("Cache-Control", "private, max-age=15, must-revalidate")
	case "catalog":
		if !catalog.ValidType(mediaType) || !strings.HasPrefix(id, "row_") {
			http.NotFound(w, r)
			return
		}
		var row *catalog.Row
		for i := range state.Rows {
			if state.Rows[i].ID == id && state.Rows[i].Type == mediaType {
				row = &state.Rows[i]
				break
			}
		}
		if row == nil {
			http.NotFound(w, r)
			return
		}
		var metas []map[string]any
		for _, item := range row.Items {
			preview := map[string]any{"id": item.ID, "type": item.Type, "name": item.Title}
			if item.Poster != "" {
				preview["poster"] = item.Poster
			}
			if item.Year > 0 {
				preview["releaseInfo"] = strconv.Itoa(item.Year)
			}
			metas = append(metas, preview)
		}
		if metas == nil {
			metas = []map[string]any{}
		}
		response = map[string]any{"metas": metas, "cacheMaxAge": 15}
		w.Header().Set("Cache-Control", "private, max-age=15, must-revalidate")
	default:
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(response)
}
