package torbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTorboxClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/torrents/checkcached":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"424e82e489c401b6caebdeae6c2ad4230301b8e0": true,
				},
			})
		case "/torrents/createtorrent":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"torrent_id": 101,
					"hash":       "424e82e489c401b6caebdeae6c2ad4230301b8e0",
					"name":       "Sample Torrent",
				},
			})
		case "/webdl/createwebdl", "/webdl/createwebdownload":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"download_id": 202,
					"url":         "https://example.com/stream.mp4",
					"name":        "Sample Stream",
				},
			})
		case "/torrents/mylist":
			idParam := r.URL.Query().Get("id")
			if idParam == "101" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success": true,
					"data": map[string]any{
						"id":             101,
						"name":           "Sample.Movie.2024.1080p.mkv",
						"size":           1073741824,
						"download_state": "completed",
						"progress":       1.0,
						"files": []map[string]any{
							{"id": 0, "name": "Sample.Movie.2024.1080p.mkv", "size": 1073741824},
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
						"name":           "Sample.Movie.2024.1080p.mkv",
						"size":           1073741824,
						"download_state": "completed",
						"progress":       1.0,
						"created_at":     "2026-01-01T12:00:00Z",
						"files": []map[string]any{
							{"id": 0, "name": "Sample.Movie.2024.1080p.mkv", "size": 1073741824},
							{"id": 1, "name": "sample.nfo", "size": 1024},
						},
					},
				},
			})
		case "/webdl/mylist":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": []map[string]any{
					{
						"id":             202,
						"name":           "DirectDownload.mp4",
						"size":           524288000,
						"download_state": "completed",
						"progress":       1.0,
						"created_at":     "2026-01-02T12:00:00Z",
						"files": []map[string]any{
							{"id": 0, "name": "DirectDownload.mp4", "size": 524288000},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := &Client{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		HTTP:    srv.Client(),
	}

	ctx := context.Background()

	// Test CheckCached
	cached, err := client.CheckCached(ctx, []string{"424e82e489c401b6caebdeae6c2ad4230301b8e0"})
	if err != nil {
		t.Fatalf("CheckCached failed: %v", err)
	}
	if !cached["424e82e489c401b6caebdeae6c2ad4230301b8e0"] {
		t.Errorf("expected hash to be marked cached")
	}

	// Test CreateTorrent
	res, err := client.CreateTorrent(ctx, "magnet:?xt=urn:btih:424e82e489c401b6caebdeae6c2ad4230301b8e0", nil)
	if err != nil {
		t.Fatalf("CreateTorrent failed: %v", err)
	}
	if res.Data.TorrentID != 101 {
		t.Errorf("expected torrent ID 101, got %d", res.Data.TorrentID)
	}

	// Test CreateWebDL
	webdlRes, err := client.CreateWebDL(ctx, "https://example.com/stream.mp4", "Sample Stream")
	if err != nil {
		t.Fatalf("CreateWebDL failed: %v", err)
	}
	if webdlRes.Data.DownloadID != 202 {
		t.Errorf("expected download ID 202, got %d", webdlRes.Data.DownloadID)
	}

	// Test GetTorrents
	torrents, err := client.GetTorrents(ctx, true)
	if err != nil {
		t.Fatalf("GetTorrents failed: %v", err)
	}
	if len(torrents) != 1 || torrents[0].ID != 101 {
		t.Errorf("expected 1 torrent with ID 101, got %v", torrents)
	}

	// Test GetTorrent
	single, err := client.GetTorrent(ctx, 101)
	if err != nil {
		t.Fatalf("GetTorrent failed: %v", err)
	}
	if single.ID != 101 {
		t.Errorf("expected torrent ID 101, got %d", single.ID)
	}

	// Test GetWebDownloads
	webdls, err := client.GetWebDownloads(ctx, true)
	if err != nil {
		t.Fatalf("GetWebDownloads failed: %v", err)
	}
	if len(webdls) != 1 || webdls[0].ID != 202 {
		t.Errorf("expected 1 webdl with ID 202, got %v", webdls)
	}

	// Test GetUploads (merged and sorted)
	uploads, err := client.GetUploads(ctx, true)
	if err != nil {
		t.Fatalf("GetUploads failed: %v", err)
	}
	if len(uploads) != 2 {
		t.Fatalf("expected 2 uploads, got %d", len(uploads))
	}
	// DirectDownload has created_at 2026-01-02, so it sorts first
	if uploads[0].ID != 202 || uploads[1].ID != 101 {
		t.Errorf("expected uploads ordered by date [202, 101], got [%d, %d]", uploads[0].ID, uploads[1].ID)
	}

	// Test BestVideoFileID
	bestID := BestVideoFileID(torrents[0].Files)
	if bestID != 0 {
		t.Errorf("expected video file ID 0, got %d", bestID)
	}

	// Test GetStreamURL
	url := client.GetStreamURL("torrent", 101, 0)
	if url == "" {
		t.Errorf("expected non-empty stream URL")
	}

	// Test FormatBytes
	if s := FormatBytes(1073741824); s != "1.00 GB" {
		t.Errorf("expected 1.00 GB, got %s", s)
	}
}
