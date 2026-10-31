package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/amxv/stremio-catalog-mcp/pkg/catalog"
)

func TestCleanUploadTitle(t *testing.T) {
	tests := []struct {
		input     string
		wantTitle string
		wantYear  int
	}{
		{"Dune.Part.Two.2024.2160p.UHD.BluRay.x265-SURCODE.mkv", "Dune Part Two", 2024},
		{"Inception.2010.1080p.BluRay.x264", "Inception", 2010},
		{"The.Dark.Knight.2008.REMASTERED.1080p.BluRay.x264", "The Dark Knight", 2008},
		{"Severance.S01.1080p.WEB-DL", "Severance", 0},
	}

	for _, tt := range tests {
		title, year := cleanUploadTitle(tt.input)
		if title != tt.wantTitle || year != tt.wantYear {
			t.Errorf("cleanUploadTitle(%q) = (%q, %d), want (%q, %d)", tt.input, title, year, tt.wantTitle, tt.wantYear)
		}
	}
}

func TestTorboxUploadsGetTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/torrents/mylist":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": []map[string]any{
					{
						"id":             10,
						"name":           "Movie.One.2024.1080p.mkv",
						"size":           1073741824,
						"download_state": "completed",
						"progress":       1.0,
						"created_at":     "2026-01-01T10:00:00Z",
					},
					{
						"id":             11,
						"name":           "Movie.Two.2024.1080p.mkv",
						"size":           2147483648,
						"download_state": "downloading",
						"progress":       0.5,
						"created_at":     "2026-01-02T10:00:00Z",
					},
				},
			})
		case "/webdl/mylist":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": []map[string]any{
					{
						"id":             20,
						"name":           "WebStream.mp4",
						"size":           524288000,
						"download_state": "completed",
						"progress":       1.0,
						"created_at":     "2026-01-03T10:00:00Z",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rt := Runtime{TorboxAPIKey: "test-key", TorboxBaseURL: srv.URL}

	// 1. Get all uploads
	res, err := rt.torboxUploadsGet(context.Background(), TorboxUploadsGetInput{})
	if err != nil {
		t.Fatalf("torboxUploadsGet failed: %v", err)
	}
	if res.Count != 3 {
		t.Fatalf("expected 3 uploads, got %d", res.Count)
	}

	// 2. Filter by status completed
	resComp, err := rt.torboxUploadsGet(context.Background(), TorboxUploadsGetInput{Status: "completed"})
	if err != nil {
		t.Fatalf("torboxUploadsGet failed: %v", err)
	}
	if resComp.Count != 2 {
		t.Fatalf("expected 2 completed uploads, got %d", resComp.Count)
	}

	// 3. Filter by type torrents
	resTorr, err := rt.torboxUploadsGet(context.Background(), TorboxUploadsGetInput{Type: "torrents"})
	if err != nil {
		t.Fatalf("torboxUploadsGet failed: %v", err)
	}
	if resTorr.Count != 2 {
		t.Fatalf("expected 2 torrents, got %d", resTorr.Count)
	}

	// 4. Test with TORBOX_API_KEY unset
	rtEmpty := Runtime{}
	resEmpty, err := rtEmpty.torboxUploadsGet(context.Background(), TorboxUploadsGetInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resEmpty.Count != 0 || resEmpty.Message == "" {
		t.Errorf("expected empty result with unconfigured message")
	}
}

func TestTorboxCatalogSyncTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/torrents/mylist":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": []map[string]any{
					{
						"id":             10,
						"name":           "Movie.One.2024.1080p.mkv",
						"size":           1073741824,
						"download_state": "completed",
						"progress":       1.0,
						"created_at":     "2026-01-01T10:00:00Z",
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
	defer srv.Close()

	rt, _, _, _, _, done := testHomeRuntime(t)
	defer done()

	rt.TorboxAPIKey = "test-key"
	rt.TorboxBaseURL = srv.URL
	rt.Manager.Resolver = mockTorboxSyncResolver{}

	syncRes, err := rt.torboxCatalogSync(context.Background(), TorboxCatalogSyncInput{
		RowName: "TorBox Movies",
		RowType: "movie",
	})
	if err != nil {
		t.Fatalf("torboxCatalogSync failed: %v", err)
	}
	if syncRes.MatchedCount != 1 {
		t.Fatalf("expected 1 matched item, got %d", syncRes.MatchedCount)
	}
	if syncRes.RowName != "TorBox Movies" {
		t.Errorf("expected row name 'TorBox Movies', got %s", syncRes.RowName)
	}

	// Verify the row exists in catalogs
	getRes, err := rt.catalogsGet(context.Background(), CatalogsGetInput{RowID: syncRes.RowID, IncludeTitles: true})
	if err != nil {
		t.Fatalf("catalogsGet failed: %v", err)
	}
	if len(getRes.Rows) != 1 || getRes.Rows[0].TitleCount != 1 {
		t.Fatalf("expected 1 title in row, got %v", getRes.Rows)
	}
	if getRes.Rows[0].Titles == nil || len(*getRes.Rows[0].Titles) != 1 || (*getRes.Rows[0].Titles)[0].ID != "tb:torrent:10" {
		t.Fatalf("expected TorBox direct ID 'tb:torrent:10', got %v", getRes.Rows[0].Titles)
	}
}

type mockTorboxSyncResolver struct{}

func (m mockTorboxSyncResolver) Resolve(ctx context.Context, mediaType string, input catalog.ItemInput) (catalog.Item, error) {
	if input.Title == "Movie One" && input.Year == 2024 {
		id := "tt1111111"
		if input.ID != "" {
			id = input.ID
		}
		return catalog.Item{
			ID:    id,
			Type:  mediaType,
			Title: "Movie One",
			Year:  2024,
		}, nil
	}
	return catalog.Item{}, nil
}
