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
		case "/webdl/createwebdl":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"download_id": 202,
					"url":         "https://example.com/stream.mp4",
					"name":        "Sample Stream",
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
}
