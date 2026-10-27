package streams

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckAvailability(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/stream/movie/tt1375666.json" {
			_ = json.NewEncoder(w).Encode(torrentioResponse{
				Streams: []torrentioStream{
					{
						Name:     "Torrentio\n1080p",
						Title:    "Inception (2010) [1080p] [BluRay]\n👤 150 💾 2.4 GB",
						InfoHash: "424e82e489c401b6caebdeae6c2ad4230301b8e0",
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	ctx := context.Background()

	// Invalid IMDb ID
	_, err := CheckAvailability(ctx, "movie", "invalid_id", "Inception", "", srv.URL)
	if err == nil {
		t.Errorf("expected error for invalid IMDb ID, got nil")
	}

	// Valid stream availability check
	res, err := CheckAvailability(ctx, "movie", "tt1375666", "Inception", "", srv.URL)
	if err != nil {
		t.Fatalf("CheckAvailability failed: %v", err)
	}

	if !res.Available {
		t.Errorf("expected title to be marked available")
	}
	if res.StreamCount != 1 {
		t.Errorf("expected 1 stream, got %d", res.StreamCount)
	}
	if res.MaxSeeders != 150 {
		t.Errorf("expected 150 seeders, got %d", res.MaxSeeders)
	}
}
