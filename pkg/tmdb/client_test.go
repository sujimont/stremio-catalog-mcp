package tmdb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTMDBClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/discover/movie":
			_ = json.NewEncoder(w).Encode(DiscoverPage{
				Page:         1,
				TotalResults: 1,
				TotalPages:   1,
				Results: []MovieResult{
					{
						ID:          550,
						Title:       "Fight Club",
						VoteAverage: 8.4,
					},
				},
			})
		case "/movie/550/external_ids":
			_ = json.NewEncoder(w).Encode(ExternalIDs{
				IMDbID: "tt0137523",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := &Client{
		APIKey:  "test-api-key",
		BaseURL: srv.URL,
		HTTP:    srv.Client(),
	}

	ctx := context.Background()

	// Test Discover
	page, err := client.Discover(ctx, DiscoverOptions{
		MediaType: "movie",
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if len(page.Results) != 1 || page.Results[0].ID != 550 {
		t.Errorf("expected 1 result with ID 550, got %+v", page.Results)
	}

	// Test ResolveIMDbID
	imdbID, err := client.ResolveIMDbID(ctx, "movie", 550)
	if err != nil {
		t.Fatalf("ResolveIMDbID failed: %v", err)
	}
	if imdbID != "tt0137523" {
		t.Errorf("expected tt0137523, got %s", imdbID)
	}
}
