package stremio

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/amxv/stremio-catalog-mcp/pkg/catalog"
	"github.com/amxv/stremio-catalog-mcp/pkg/torbox"
)

const (
	AddonID           = "xyz.sujimont.stremio-catalog-mcp"
	LegacyAddonID     = "xyz.ashray.stremio-catalog-mcp"
	TorboxCatalogID   = "torbox_uploads"
	TorboxCatalogName = "TorBox Cloud"
)

// IsOurAddon checks if an addon ID belongs to this catalog MCP server (current or legacy).
func IsOurAddon(id string) bool {
	return id == AddonID || id == LegacyAddonID
}

type Manifest struct {
	ID          string    `json:"id"`
	Version     string    `json:"version"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Resources   []any     `json:"resources"`
	Types       []string  `json:"types"`
	IDPrefixes  []string  `json:"idPrefixes,omitempty"`
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

type ManifestOptions struct {
	HasTorbox bool
}

type ManifestOption func(*ManifestOptions)

func WithTorbox(enabled bool) ManifestOption {
	return func(o *ManifestOptions) {
		o.HasTorbox = enabled
	}
}

func BuildManifest(state catalog.State, opts ...ManifestOption) Manifest {
	var cfg ManifestOptions
	for _, opt := range opts {
		opt(&cfg)
	}

	resources := []any{"catalog"}
	types := []string{"movie", "series"}
	if cfg.HasTorbox {
		resources = append(resources,
			map[string]any{
				"name":       "meta",
				"types":      []string{"movie", "series", "other"},
				"idPrefixes": []string{"tb:"},
			},
			map[string]any{
				"name":       "stream",
				"types":      []string{"movie", "series", "other"},
				"idPrefixes": []string{"tb:"},
			},
		)
		types = append(types, "other")
	}

	manifest := Manifest{
		ID: AddonID, Version: fmt.Sprintf("0.2.%d", state.Revision), Name: "Stremio Catalog MCP",
		Description: "Private catalog rows curated by your MCP agent with TorBox cloud integration. Streams and metadata stay with your existing addons.",
		Resources:   resources, Types: types, Catalogs: []Catalog{},
	}
	if cfg.HasTorbox {
		manifest.IDPrefixes = []string{"tb:"}
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
	return catalog.NormalizeProfile(p)
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
	TorboxAPIKey      string
	TorboxClient      *torbox.Client
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
		} else if len(remaining) == 3 && (remaining[0] == "catalog" || remaining[0] == "meta" || remaining[0] == "stream") && strings.HasSuffix(remaining[2], ".json") {
			resource, mediaType, id = remaining[0], remaining[1], strings.TrimSuffix(remaining[2], ".json")
		}
	} else if len(parts) == 2 && parts[0] == "api" && (parts[1] == "stremio" || parts[1] == "mcp") {
		q := r.URL.Query()
		token, resource, mediaType, id, profile = q.Get("token"), q.Get("resource"), q.Get("type"), q.Get("id"), q.Get("profile")
	}

	if unescaped, err := url.PathUnescape(id); err == nil && unescaped != "" {
		id = unescaped
	}
	if unescaped, err := url.QueryUnescape(id); err == nil && unescaped != "" {
		id = unescaped
	}
	if !EqualToken(h.FeedToken, token) {
		http.NotFound(w, r)
		return
	}
	profile = NormalizeProfile(profile)
	mgr := h.Manager
	if profile != "" && profile != "default" && h.ManagerForProfile != nil {
		mgr = h.ManagerForProfile(profile)
	}
	state, err := mgr.Read(r.Context())
	if err != nil {
		if resource != "stream" && resource != "meta" {
			http.Error(w, "Catalog storage unavailable", http.StatusServiceUnavailable)
			return
		}
	}

	torboxKey := h.TorboxAPIKey
	if torboxKey == "" {
		torboxKey = os.Getenv("TORBOX_API_KEY")
	}
	hasTorbox := torboxKey != "" || h.TorboxClient != nil

	var response any
	switch resource {
	case "manifest":
		response = BuildManifest(state, WithTorbox(hasTorbox))
		w.Header().Set("Cache-Control", "private, max-age=15, must-revalidate")
	case "catalog":
		if id == TorboxCatalogID || id == "torbox_cloud" || id == "torbox_movies" || id == "torbox_series" {
			if !hasTorbox {
				http.NotFound(w, r)
				return
			}
			tb := h.TorboxClient
			if tb == nil {
				tb = torbox.NewClient(torboxKey)
			}
			uploads, err := tb.GetUploads(r.Context(), false)
			if err != nil {
				http.Error(w, "TorBox unavailable: "+err.Error(), http.StatusBadGateway)
				return
			}
			var metas []map[string]any
			for _, u := range uploads {
				cleanTitle, year := torbox.CleanUploadTitle(u.Name)
				if cleanTitle == "" {
					cleanTitle = u.Name
				}
				isSeries := false
				if _, _, ok := torbox.ParseSeasonEpisode(u.Name); ok {
					isSeries = true
				} else {
					for _, f := range u.Files {
						if _, _, ok := torbox.ParseSeasonEpisode(f.Name); ok {
							isSeries = true
							break
						}
					}
				}

				if id == "torbox_movies" && isSeries {
					continue
				}
				if id == "torbox_series" && !isSeries {
					continue
				}

				itemType := mediaType
				if itemType == "" || itemType == "other" {
					if isSeries {
						itemType = "series"
					} else {
						itemType = "movie"
					}
				}

				preview := map[string]any{
					"id":          u.StremioID(),
					"type":        itemType,
					"name":        cleanTitle,
					"releaseInfo": u.SizeFormatted + " • " + u.Status,
					"description": fmt.Sprintf("TorBox %s\nOriginal: %s\nSize: %s\nStatus: %s\nProgress: %.0f%%\nFiles: %d",
						strings.ToUpper(u.Type), u.Name, u.SizeFormatted, u.Status, u.Progress*100, len(u.Files)),
				}
				if year > 0 {
					preview["releaseInfo"] = strconv.Itoa(year) + " • " + u.SizeFormatted
				}
				metas = append(metas, preview)
			}
			if metas == nil {
				metas = []map[string]any{}
			}
			response = map[string]any{"metas": metas, "cacheMaxAge": 30}
			w.Header().Set("Cache-Control", "private, max-age=30, must-revalidate")
			break
		}

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

	case "meta":
		if !strings.HasPrefix(id, "tb:") || !hasTorbox {
			http.NotFound(w, r)
			return
		}
		tb := h.TorboxClient
		if tb == nil {
			tb = torbox.NewClient(torboxKey)
		}
		parts := strings.Split(id, ":")
		if len(parts) < 3 {
			http.NotFound(w, r)
			return
		}
		itemType := parts[1]
		itemID, err := strconv.Atoi(parts[2])
		if err != nil {
			http.NotFound(w, r)
			return
		}

		var (
			rawName    string
			size       int64
			status     string
			progress   float64
			files      []torbox.TorrentFile
			metaVideos []map[string]any
			poster     string
		)

		var foundItem *catalog.Item
		for _, r := range state.Rows {
			for _, it := range r.Items {
				if it.ID == id {
					foundItem = &it
					break
				}
			}
			if foundItem != nil {
				break
			}
		}
		if foundItem != nil {
			rawName = foundItem.Title
			poster = foundItem.Poster
		}

		if itemType == "torrent" {
			t, err := tb.GetTorrent(r.Context(), itemID)
			if err == nil && t != nil {
				rawName = t.Name
				size, status, progress, files = t.Size, t.DownloadState, t.Progress, t.Files
			} else if foundItem == nil {
				http.NotFound(w, r)
				return
			}
		} else if itemType == "webdl" {
			uploads, err := tb.GetUploads(r.Context(), false)
			if err == nil {
				for _, u := range uploads {
					if u.Type == "webdl" && u.ID == itemID {
						rawName = u.Name
						size, status, progress, files = u.Size, u.Status, u.Progress, u.Files
						break
					}
				}
			}
			if rawName == "" && foundItem == nil {
				http.NotFound(w, r)
				return
			}
		}

		if rawName == "" {
			rawName = fmt.Sprintf("TorBox %s #%d", strings.ToUpper(itemType), itemID)
		}

		cleanTitle, year := torbox.CleanUploadTitle(rawName)
		if foundItem != nil && foundItem.Title != "" {
			cleanTitle = foundItem.Title
		} else if cleanTitle == "" {
			cleanTitle = rawName
		}
		if foundItem != nil && foundItem.Year > 0 {
			year = foundItem.Year
		}
		if poster == "" && foundItem != nil {
			poster = foundItem.Poster
		}

		defaultSeason := 1
		if s, _, ok := torbox.ParseSeasonEpisode(rawName); ok && s > 0 {
			defaultSeason = s
		} else if s := torbox.ExtractSeason(rawName); s > 0 {
			defaultSeason = s
		}

		if len(files) > 0 {
			videoIdx := 0
			for _, f := range files {
				ext := strings.ToLower(f.Name)
				if idx := strings.LastIndex(ext, "."); idx != -1 {
					ext = ext[idx:]
				}
				if isVideoExtension(ext) || len(files) == 1 {
					videoIdx++
					vID := fmt.Sprintf("tb:%s:%d:%d", itemType, itemID, f.ID)
					season := defaultSeason
					episode := videoIdx
					if s, ep, ok := torbox.ParseSeasonEpisode(f.Name); ok {
						if s > 0 {
							season = s
						}
						if ep > 0 {
							episode = ep
						}
					} else if ep := torbox.ExtractEpisode(f.Name); ep > 0 {
						episode = ep
					}

					cleanFile := f.Name
					if slashIdx := strings.LastIndex(cleanFile, "/"); slashIdx != -1 {
						cleanFile = cleanFile[slashIdx+1:]
					}

					vEntry := map[string]any{
						"id":       vID,
						"title":    fmt.Sprintf("%s (%s)", cleanFile, torbox.FormatBytes(f.Size)),
						"season":   season,
						"episode":  episode,
						"released": time.Now().UTC().Format(time.RFC3339),
					}
					metaVideos = append(metaVideos, vEntry)
				}
			}
		}
		if len(metaVideos) == 0 {
			metaVideos = append(metaVideos, map[string]any{
				"id":       fmt.Sprintf("tb:%s:%d:0", itemType, itemID),
				"title":    cleanTitle,
				"season":   1,
				"episode":  1,
				"released": time.Now().UTC().Format(time.RFC3339),
			})
		}

		sort.Slice(metaVideos, func(i, j int) bool {
			sI, _ := metaVideos[i]["season"].(int)
			sJ, _ := metaVideos[j]["season"].(int)
			if sI != sJ {
				return sI < sJ
			}
			epI, _ := metaVideos[i]["episode"].(int)
			epJ, _ := metaVideos[j]["episode"].(int)
			return epI < epJ
		})

		outType := mediaType
		if outType == "" || outType == "other" {
			if len(metaVideos) > 1 {
				outType = "series"
			} else {
				outType = "movie"
			}
		}

		metaDetail := map[string]any{
			"id":          id,
			"type":        outType,
			"name":        cleanTitle,
			"description": fmt.Sprintf("TorBox Cloud Upload\nType: %s\nOriginal Name: %s\nSize: %s\nStatus: %s\nProgress: %.0f%%\nTotal Files: %d", strings.ToUpper(itemType), rawName, torbox.FormatBytes(size), status, progress*100, len(files)),
			"videos":      metaVideos,
		}
		if poster != "" {
			metaDetail["poster"] = poster
		}
		if year > 0 {
			metaDetail["releaseInfo"] = strconv.Itoa(year)
		} else {
			metaDetail["releaseInfo"] = torbox.FormatBytes(size) + " • " + status
		}
		if len(metaVideos) > 0 {
			if firstID, ok := metaVideos[0]["id"].(string); ok && firstID != "" {
				metaDetail["behaviorHints"] = map[string]any{
					"defaultVideoId": firstID,
				}
			}
		}
		response = map[string]any{"meta": metaDetail, "cacheMaxAge": 60}
		w.Header().Set("Cache-Control", "private, max-age=60, must-revalidate")

	case "stream":
		if !strings.HasPrefix(id, "tb:") || !hasTorbox {
			http.NotFound(w, r)
			return
		}
		tb := h.TorboxClient
		if tb == nil {
			tb = torbox.NewClient(torboxKey)
		}
		parts := strings.Split(id, ":")
		if len(parts) < 3 {
			http.NotFound(w, r)
			return
		}
		itemType := parts[1]
		itemID, err := strconv.Atoi(parts[2])
		if err != nil {
			http.NotFound(w, r)
			return
		}
		fileID := -1
		if len(parts) >= 4 {
			lastPart := parts[len(parts)-1]
			if fid, err := strconv.Atoi(lastPart); err == nil {
				fileID = fid
			}
		}

		if fileID < 0 {
			if itemType == "torrent" {
				if t, err := tb.GetTorrent(r.Context(), itemID); err == nil && t != nil {
					fileID = torbox.BestVideoFileID(t.Files)
				}
			} else if itemType == "webdl" {
				if uploads, err := tb.GetUploads(r.Context(), false); err == nil {
					for _, u := range uploads {
						if u.Type == "webdl" && u.ID == itemID {
							fileID = torbox.BestVideoFileID(u.Files)
							break
						}
					}
				}
			}
		}

		streamURL := tb.GetStreamURL(itemType, itemID, fileID)
		if streamURL == "" {
			http.Error(w, "Unable to generate TorBox stream link", http.StatusBadGateway)
			return
		}

		title := fmt.Sprintf("TorBox Cloud [%s #%d]", strings.ToUpper(itemType), itemID)
		streams := []map[string]any{
			{
				"name":  "TorBox Cloud",
				"title": title + "\nDirect Play",
				"url":   streamURL,
			},
		}
		response = map[string]any{"streams": streams, "cacheMaxAge": 300}
		w.Header().Set("Cache-Control", "private, max-age=300, must-revalidate")

	default:
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(response)
}

func isVideoExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".mkv", ".mp4", ".avi", ".mov", ".wmv", ".webm", ".m4v", ".ts", ".flv":
		return true
	default:
		return false
	}
}
