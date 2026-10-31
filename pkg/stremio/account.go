package stremio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/amxv/stremio-catalog-mcp/pkg/catalog"
)

// AccountAPI encapsulates Stremio's own account addon collection API. It is
// intentionally isolated because it isn't a separately versioned public API.
type AccountAPI struct {
	BaseURL string
	AuthKey string
	Client  *http.Client
	// TorboxEnabled includes TorBox cloud catalogs and streaming resources in manifest sync.
	TorboxEnabled bool
	// BeforeReplace must successfully persist a private snapshot of the current
	// account collection before the whole-collection Stremio API write proceeds.
	BeforeReplace func(context.Context, []Descriptor) error
}

type Descriptor struct {
	Manifest      json.RawMessage `json:"manifest"`
	TransportURL  string          `json:"transportUrl"`
	TransportName string          `json:"transportName,omitempty"`
	Flags         json.RawMessage `json:"flags,omitempty"`
	// Preserve fields added by Stremio or other addons when rewriting the
	// account collection. The API uses whole-collection replacement.
	extra map[string]json.RawMessage
}

func (d *Descriptor) UnmarshalJSON(data []byte) error {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	type fields struct {
		Manifest      json.RawMessage `json:"manifest"`
		TransportURL  string          `json:"transportUrl"`
		TransportName string          `json:"transportName"`
		Flags         json.RawMessage `json:"flags"`
	}
	var f fields
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	d.Manifest, d.TransportURL, d.TransportName, d.Flags = f.Manifest, f.TransportURL, f.TransportName, f.Flags
	for _, key := range []string{"manifest", "transportUrl", "transportName", "flags"} {
		delete(all, key)
	}
	d.extra = all
	return nil
}

func (d Descriptor) MarshalJSON() ([]byte, error) {
	result := make(map[string]json.RawMessage, len(d.extra)+4)
	for key, value := range d.extra {
		result[key] = value
	}
	result["manifest"] = d.Manifest
	url, _ := json.Marshal(d.TransportURL)
	result["transportUrl"] = url
	if d.TransportName != "" {
		name, _ := json.Marshal(d.TransportName)
		result["transportName"] = name
	}
	if len(d.Flags) != 0 {
		result["flags"] = d.Flags
	}
	return json.Marshal(result)
}

func (a AccountAPI) call(ctx context.Context, method string, payload map[string]any) (json.RawMessage, error) {
	if a.AuthKey == "" {
		return nil, errors.New("Stremio account is not linked")
	}
	base := a.BaseURL
	if base == "" {
		base = "https://api.strem.io"
	}
	if !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://127.0.0.1:") {
		return nil, errors.New("invalid Stremio API origin")
	}
	payload["type"], payload["authKey"] = method, a.AuthKey
	encoded, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/api/"+strings.ToLower(method[:1])+method[1:], bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Stremio account request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Stremio account API returned HTTP %d", resp.StatusCode)
	}
	var body struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("invalid Stremio response: %w", err)
	}
	if body.Error != nil {
		return nil, fmt.Errorf("Stremio account API error %d: %s", body.Error.Code, body.Error.Message)
	}
	if len(body.Result) == 0 || string(body.Result) == "null" {
		return nil, errors.New("Stremio account API returned no result")
	}
	return body.Result, nil
}

func (a AccountAPI) Get(ctx context.Context) ([]Descriptor, error) {
	raw, err := a.call(ctx, "AddonCollectionGet", map[string]any{"update": true})
	if err != nil {
		return nil, err
	}
	var result struct {
		Addons []Descriptor `json:"addons"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.Addons == nil {
		return nil, errors.New("unexpected Stremio addonCollectionGet response (expected result.addons)")
	}
	for _, addon := range result.Addons {
		if addon.TransportURL == "" || len(addon.Manifest) == 0 {
			return nil, errors.New("refusing to rewrite malformed Stremio addon collection")
		}
	}
	return result.Addons, nil
}

func (a AccountAPI) Set(ctx context.Context, addons []Descriptor) error {
	if len(addons) == 0 {
		return errors.New("refusing to replace Stremio account with an empty addon collection")
	}
	_, err := a.call(ctx, "AddonCollectionSet", map[string]any{"addons": addons})
	return err
}

func descriptorID(d Descriptor) string {
	var manifest struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(d.Manifest, &manifest)
	return manifest.ID
}

// Sync only replaces our own descriptor while retaining ordering, flags and
// all metadata for installed streaming addons and Cinemeta.
func (a AccountAPI) Sync(ctx context.Context, state catalog.State, manifestURL string) error {
	current, err := a.Get(ctx)
	if err != nil {
		return err
	}
	if len(current) == 0 {
		return errors.New("refusing to synchronize an empty Stremio add-on collection")
	}
	manifest, err := json.Marshal(BuildManifest(state, WithTorbox(a.TorboxEnabled)))
	if err != nil {
		return err
	}
	position := -1
	for i, entry := range current {
		if IsOurAddon(descriptorID(entry)) {
			if position != -1 {
				return errors.New("duplicate installed instances of our addon; refusing account rewrite")
			}
			position = i
		}
	}
	updated := append([]Descriptor(nil), current...)
	if position == -1 {
		updated = append(updated, Descriptor{
			Manifest: manifest, TransportURL: manifestURL, TransportName: "http",
			Flags: json.RawMessage(`{"official":false,"protected":false}`),
		})
	} else {
		entry := updated[position]
		entry.Manifest = manifest
		entry.TransportURL = manifestURL
		entry.TransportName = "http"
		updated[position] = entry
	}
	// Abort if another client edited the account since our original read.
	// The account endpoint has no transaction/CAS, so this reduces but cannot
	// completely eliminate races with other Stremio devices.
	latest, err := a.Get(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(latest, current) {
		return errors.New("Stremio add-on collection changed while syncing; retry to avoid overwriting other addons")
	}
	if a.BeforeReplace != nil {
		if err := a.BeforeReplace(ctx, latest); err != nil {
			return fmt.Errorf("refusing Stremio write because the account backup failed: %w", err)
		}
	}
	if err := a.Set(ctx, updated); err != nil {
		return err
	}
	verified, err := a.Get(ctx)
	if err != nil {
		return fmt.Errorf("Stremio write completed but verification failed: %w", err)
	}
	var found int
	for _, entry := range verified {
		if IsOurAddon(descriptorID(entry)) {
			found++
			var actual Manifest
			expected := BuildManifest(state, WithTorbox(a.TorboxEnabled))
			if json.Unmarshal(entry.Manifest, &actual) != nil || !reflect.DeepEqual(actual.Catalogs, expected.Catalogs) {
				return errors.New("Stremio reported an older manifest; sync may need retry")
			}
		}
	}
	if found != 1 || len(verified) != len(updated) {
		return errors.New("Stremio addon collection unexpectedly changed after sync")
	}
	for i, entry := range verified {
		if !IsOurAddon(descriptorID(entry)) && !reflect.DeepEqual(entry, current[i]) {
			return errors.New("unrelated addon descriptor differs from original collection after sync")
		}
	}
	return nil
}

type LibraryItemState struct {
	LastWatched        string `json:"lastWatched"`
	TimeWatched        int64  `json:"timeWatched"`
	TimeOffset         int64  `json:"timeOffset"`
	OverallTimeWatched int64  `json:"overallTimeWatched"`
	TimesWatched       int    `json:"timesWatched"`
	FlaggedWatched     int    `json:"flaggedWatched"`
	Duration           int64  `json:"duration"`
	VideoID            string `json:"video_id"`
}

type LibraryItemDetail struct {
	ID     string           `json:"_id"`
	Name   string           `json:"name"`
	Type   string           `json:"type"`
	Poster string           `json:"poster,omitempty"`
	Year   string           `json:"year,omitempty"`
	State  LibraryItemState `json:"state"`
}

type WatchedItem struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Type            string  `json:"type"`
	Poster          string  `json:"poster,omitempty"`
	Year            string  `json:"year,omitempty"`
	LastWatched     string  `json:"last_watched,omitempty"`
	TimesWatched    int     `json:"times_watched"`
	ProgressPercent float64 `json:"progress_percent,omitempty"`
	Completed       bool    `json:"completed"`
}

// GetWatchHistory retrieves recently watched library items via Stremio's Datastore API.
func (a AccountAPI) GetWatchHistory(ctx context.Context, limit int, onlyCompleted bool) ([]WatchedItem, error) {
	if a.AuthKey == "" {
		return nil, errors.New("Stremio account is not linked")
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	raw, err := a.call(ctx, "DatastoreGet", map[string]any{"collection": "libraryItem"})
	if err != nil {
		return nil, err
	}
	var summaryList [][]json.RawMessage
	if err := json.Unmarshal(raw, &summaryList); err != nil {
		return nil, fmt.Errorf("unexpected datastore summary: %w", err)
	}

	type tuple struct {
		id    string
		mtime int64
	}
	var tuples []tuple
	for _, entry := range summaryList {
		if len(entry) < 2 {
			continue
		}
		var id string
		var mtime int64
		if json.Unmarshal(entry[0], &id) == nil && json.Unmarshal(entry[1], &mtime) == nil {
			if strings.HasPrefix(id, "tt") {
				tuples = append(tuples, tuple{id: id, mtime: mtime})
			}
		}
	}

	// Sort tuples descending by timestamp (newest first)
	sort.Slice(tuples, func(i, j int) bool {
		return tuples[i].mtime > tuples[j].mtime
	})

	fetchCount := limit * 2
	if onlyCompleted {
		fetchCount = limit * 4
	}
	if fetchCount > 100 {
		fetchCount = 100
	}
	if fetchCount > len(tuples) {
		fetchCount = len(tuples)
	}
	if fetchCount == 0 {
		return []WatchedItem{}, nil
	}

	targetIDs := make([]string, fetchCount)
	for i := 0; i < fetchCount; i++ {
		targetIDs[i] = tuples[i].id
	}

	detailRaw, err := a.call(ctx, "DatastoreGet", map[string]any{
		"collection": "libraryItem",
		"ids":        targetIDs,
	})
	if err != nil {
		return nil, err
	}

	var details []LibraryItemDetail
	if err := json.Unmarshal(detailRaw, &details); err != nil {
		return nil, fmt.Errorf("unexpected datastore details: %w", err)
	}

	idMap := make(map[string]LibraryItemDetail, len(details))
	for _, d := range details {
		idMap[d.ID] = d
	}

	var results []WatchedItem
	for _, t := range targetIDs {
		d, found := idMap[t]
		if !found {
			continue
		}
		completed := d.State.TimesWatched > 0 || d.State.FlaggedWatched > 0
		var progress float64
		if d.State.Duration > 0 {
			progress = float64(d.State.TimeOffset) / float64(d.State.Duration) * 100
			if progress >= 85.0 {
				completed = true
			}
		}
		if onlyCompleted && !completed {
			continue
		}
		results = append(results, WatchedItem{
			ID:              d.ID,
			Name:            d.Name,
			Type:            d.Type,
			Poster:          d.Poster,
			Year:            d.Year,
			LastWatched:     d.State.LastWatched,
			TimesWatched:    d.State.TimesWatched,
			ProgressPercent: progress,
			Completed:       completed,
		})
		if len(results) >= limit {
			break
		}
	}

	return results, nil
}

type InstalledAddonInfo struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Description  string   `json:"description,omitempty"`
	TransportURL string   `json:"transport_url"`
	Types        []string `json:"types,omitempty"`
	Protected    bool     `json:"protected"`
}

// GetTorrentioStreamBase returns the user's installed Torrentio stream base URL.
func (a AccountAPI) GetTorrentioStreamBase(ctx context.Context) string {
	addons, err := a.Get(ctx)
	if err != nil {
		return ""
	}
	for _, entry := range addons {
		if strings.Contains(entry.TransportURL, "torrentio.strem.fun") {
			return strings.TrimSuffix(entry.TransportURL, "/manifest.json")
		}
	}
	return ""
}

// ListInstalledAddons returns a user-friendly summary of all installed addons.
func (a AccountAPI) ListInstalledAddons(ctx context.Context) ([]InstalledAddonInfo, error) {
	addons, err := a.Get(ctx)
	if err != nil {
		return nil, err
	}

	var results []InstalledAddonInfo
	for _, entry := range addons {
		var m struct {
			ID          string   `json:"id"`
			Name        string   `json:"name"`
			Version     string   `json:"version"`
			Description string   `json:"description"`
			Types       []string `json:"types"`
		}
		_ = json.Unmarshal(entry.Manifest, &m)

		var flags struct {
			Official  bool `json:"official"`
			Protected bool `json:"protected"`
		}
		_ = json.Unmarshal(entry.Flags, &flags)

		results = append(results, InstalledAddonInfo{
			ID:           m.ID,
			Name:         m.Name,
			Version:      m.Version,
			Description:  m.Description,
			TransportURL: entry.TransportURL,
			Types:        m.Types,
			Protected:    flags.Protected,
		})
	}
	return results, nil
}

// InstallAddon fetches the addon's remote manifest and installs it in the Stremio account.
func (a AccountAPI) InstallAddon(ctx context.Context, manifestURL string) (*InstalledAddonInfo, error) {
	manifestURL = strings.TrimSpace(manifestURL)
	if strings.HasPrefix(manifestURL, "stremio://") {
		manifestURL = "https://" + strings.TrimPrefix(manifestURL, "stremio://")
	}
	if !strings.HasPrefix(manifestURL, "http://") && !strings.HasPrefix(manifestURL, "https://") {
		return nil, fmt.Errorf("invalid manifest URL %q (must be http or https)", manifestURL)
	}

	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch addon manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("addon server returned HTTP %d", resp.StatusCode)
	}

	manifestBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var m struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Version     string   `json:"version"`
		Description string   `json:"description"`
		Types       []string `json:"types"`
	}
	if err := json.Unmarshal(manifestBytes, &m); err != nil || m.ID == "" {
		return nil, fmt.Errorf("invalid Stremio manifest at %s", manifestURL)
	}

	current, err := a.Get(ctx)
	if err != nil {
		return nil, err
	}

	newDesc := Descriptor{
		Manifest:      manifestBytes,
		TransportURL:  manifestURL,
		TransportName: "http",
		Flags:         json.RawMessage(`{"official":false,"protected":false}`),
	}

	updated := make([]Descriptor, 0, len(current)+1)
	replaced := false
	for _, entry := range current {
		if descriptorID(entry) == m.ID {
			updated = append(updated, newDesc)
			replaced = true
		} else {
			updated = append(updated, entry)
		}
	}
	if !replaced {
		updated = append(updated, newDesc)
	}

	if a.BeforeReplace != nil {
		if err := a.BeforeReplace(ctx, current); err != nil {
			return nil, fmt.Errorf("backup failed: %w", err)
		}
	}

	if err := a.Set(ctx, updated); err != nil {
		return nil, fmt.Errorf("failed to update Stremio account: %w", err)
	}

	return &InstalledAddonInfo{
		ID:           m.ID,
		Name:         m.Name,
		Version:      m.Version,
		Description:  m.Description,
		TransportURL: manifestURL,
		Types:        m.Types,
	}, nil
}

// UninstallAddon removes an addon from the Stremio account by ID or TransportURL.
func (a AccountAPI) UninstallAddon(ctx context.Context, addonIDOrURL string) error {
	addonIDOrURL = strings.TrimSpace(addonIDOrURL)
	if addonIDOrURL == "com.linvo.cinemeta" {
		return fmt.Errorf("refusing to uninstall Cinemeta (required default catalog)")
	}

	current, err := a.Get(ctx)
	if err != nil {
		return err
	}

	var remaining []Descriptor
	removed := false

	for _, entry := range current {
		id := descriptorID(entry)
		if id == addonIDOrURL || entry.TransportURL == addonIDOrURL {
			removed = true
			continue
		}
		remaining = append(remaining, entry)
	}

	if !removed {
		return fmt.Errorf("addon %q is not installed in your Stremio account", addonIDOrURL)
	}

	if len(remaining) == 0 {
		return fmt.Errorf("refusing to empty addon collection")
	}

	if a.BeforeReplace != nil {
		if err := a.BeforeReplace(ctx, current); err != nil {
			return fmt.Errorf("backup failed: %w", err)
		}
	}

	return a.Set(ctx, remaining)
}
