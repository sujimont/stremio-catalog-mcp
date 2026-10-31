package stremio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/amxv/stremio-catalog-mcp/pkg/catalog"
	"github.com/amxv/stremio-catalog-mcp/pkg/torbox"
)

func TestCatalogAddOnProtocol(t *testing.T) {
	store := &catalog.MemoryStore{}
	state := catalog.State{Revision: 3, Rows: []catalog.Row{{ID: "row_abc", Name: "Mysteries", Type: "series", Items: []catalog.Item{{ID: "tt0491738", Type: "series", Title: "Psych", Poster: "https://poster.example/psych.jpg"}}}}}
	raw, _ := json.Marshal(state)
	_, _ = store.CAS(context.Background(), "", string(raw))
	h := Handler{Manager: catalog.Manager{Store: store}, FeedToken: "test-feed-token"}
	for _, test := range []struct {
		Path     string
		Status   int
		Contains string
	}{
		{"/stremio/test-feed-token/manifest.json", 200, `"name":"Mysteries"`},
		{"/stremio/test-feed-token/v3/manifest.json", 200, `"version":"0.1.3"`},
		{"/stremio/test-feed-token/catalog/series/row_abc.json", 200, `"id":"tt0491738"`},
		{"/stremio/test-feed-token/v3/catalog/series/row_abc.json", 200, `"name":"Psych"`},
		{"/stremio/test-feed-token/catalog/movie/row_abc.json", 404, ""},
		{"/stremio/test-feed-token/catalog/series/nope.json", 404, ""},
		{"/stremio/wrong/manifest.json", 404, ""},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", test.Path, nil))
		if rec.Code != test.Status || !strings.Contains(rec.Body.String(), test.Contains) {
			t.Errorf("%s got HTTP %d: %s", test.Path, rec.Code, rec.Body.String())
		}
	}
}

func TestAccountSyncPreservesStreamingAddons(t *testing.T) {
	protected := json.RawMessage(`{"id":"com.linvo.cinemeta","name":"Cinemeta","catalogs":[]}`)
	streaming := json.RawMessage(`{"id":"test.streaming","name":"Stream Addon","resources":["stream"],"catalogs":[]}`)
	current := []Descriptor{
		{Manifest: protected, TransportURL: "https://v3-cinemeta.strem.io/manifest.json", Flags: json.RawMessage(`{"protected":true,"official":true}`)},
		{Manifest: streaming, TransportURL: "https://stream.example/private/manifest.json", Flags: json.RawMessage(`{"protected":false}`)},
	}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var req struct {
			Type    string       `json:"type"`
			AuthKey string       `json:"authKey"`
			Addons  []Descriptor `json:"addons"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AuthKey != "test-auth" {
			http.Error(w, "bad request", 400)
			return
		}
		switch req.Type {
		case "AddonCollectionGet":
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"addons": current}})
		case "AddonCollectionSet":
			current = req.Addons
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"success": true}})
		default:
			http.Error(w, "unexpected method", 400)
		}
	}))
	defer srv.Close()
	api := AccountAPI{BaseURL: srv.URL, AuthKey: "test-auth"}
	backupCalls := 0
	api.BeforeReplace = func(_ context.Context, items []Descriptor) error {
		backupCalls++
		if len(items) != 2 && len(items) != 3 {
			t.Fatalf("unexpected backup snapshot: %d", len(items))
		}
		return nil
	}
	state := catalog.State{Revision: 1, Rows: []catalog.Row{{ID: "row_abc", Name: "Comedy", Type: "series", Items: []catalog.Item{}}}}
	if err := api.Sync(context.Background(), state, "https://my.example/stremio/secret/v1/manifest.json"); err != nil {
		t.Fatal(err)
	}
	if len(current) != 3 || string(current[0].Manifest) != string(protected) || string(current[1].Manifest) != string(streaming) || current[1].TransportURL != "https://stream.example/private/manifest.json" {
		t.Fatalf("account contents overwritten: %+v", current)
	}
	state.Rows[0].Name = "Whodunits"
	state.Revision++
	if err := api.Sync(context.Background(), state, "https://my.example/stremio/secret/v2/manifest.json"); err != nil {
		t.Fatal(err)
	}
	if len(current) != 3 || current[2].TransportURL != "https://my.example/stremio/secret/v2/manifest.json" {
		t.Fatalf("did not update own descriptor: %+v", current[2])
	}
	if backupCalls != 2 {
		t.Fatalf("expected two pre-write snapshots, received %d", backupCalls)
	}
}

func TestAccountDescriptorUnknownFieldsRoundTrip(t *testing.T) {
	before := []byte(`{"manifest":{"id":"example.stream"},"transportUrl":"https://stream.example/manifest.json","transportName":"http","flags":{"protected":false},"customAddonConfig":{"filters":["x","y"]},"settingsVersion":42}`)
	var descriptor Descriptor
	if err := json.Unmarshal(before, &descriptor); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var expected, actual map[string]any
	_ = json.Unmarshal(before, &expected)
	_ = json.Unmarshal(after, &actual)
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("unknown addon fields lost: %s", after)
	}
}

func TestTorboxAddonProtocol(t *testing.T) {
	torboxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/torrents/mylist":
			if r.URL.Query().Get("id") == "101" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success": true,
					"data": map[string]any{
						"id":             101,
						"name":           "Interstellar.2014.1080p.mkv",
						"size":           2147483648,
						"download_state": "completed",
						"progress":       1.0,
						"files": []map[string]any{
							{"id": 0, "name": "Interstellar.2014.1080p.mkv", "size": 2147483648},
						},
					},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": []map[string]any{
					{
						"id":             101,
						"name":           "Interstellar.2014.1080p.mkv",
						"size":           2147483648,
						"download_state": "completed",
						"progress":       1.0,
						"created_at":     "2026-01-01T12:00:00Z",
						"files": []map[string]any{
							{"id": 0, "name": "Interstellar.2014.1080p.mkv", "size": 2147483648},
						},
					},
				},
			})
		case "/webdl/mylist":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    []map[string]any{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer torboxServer.Close()

	tbClient := &torbox.Client{
		APIKey:  "test-key",
		BaseURL: torboxServer.URL,
		HTTP:    torboxServer.Client(),
	}

	store := &catalog.MemoryStore{}
	state := catalog.State{Revision: 1, Rows: []catalog.Row{}}
	raw, _ := json.Marshal(state)
	_, _ = store.CAS(context.Background(), "", string(raw))
	h := Handler{
		Manager:      catalog.Manager{Store: store},
		FeedToken:    "test-token",
		TorboxClient: tbClient,
	}

	for _, test := range []struct {
		Path     string
		Status   int
		Contains string
	}{
		{"/stremio/test-token/manifest.json", 200, `"id":"torbox_uploads"`},
		{"/stremio/test-token/manifest.json", 200, `"name":"TorBox Cloud"`},
		{"/stremio/test-token/catalog/other/torbox_uploads.json", 200, `"id":"tb:torrent:101"`},
		{"/stremio/test-token/catalog/other/torbox_uploads.json", 200, `"name":"Interstellar.2014.1080p.mkv"`},
		{"/stremio/test-token/meta/other/tb:torrent:101.json", 200, `"videos"`},
		{"/stremio/test-token/meta/other/tb:torrent:101.json", 200, `"tb:torrent:101:0"`},
		{"/stremio/test-token/stream/other/tb:torrent:101:0.json", 200, `"streams"`},
		{"/stremio/test-token/stream/other/tb:torrent:101:0.json", 200, "torrent_id=101"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", test.Path, nil))
		if rec.Code != test.Status || !strings.Contains(rec.Body.String(), test.Contains) {
			t.Errorf("%s got HTTP %d: %s", test.Path, rec.Code, rec.Body.String())
		}
	}
}
