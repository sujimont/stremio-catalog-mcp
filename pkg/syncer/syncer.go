package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/amxv/stremio-catalog-mcp/pkg/catalog"
	"github.com/amxv/stremio-catalog-mcp/pkg/stremio"
)

const (
	MovieRulzBase = "https://movierulz-stremio.onrender.com/eyJjYXRhbG9ncyI6WyJtcnVsel90YW1pbCIsIm1ydWx6X2hpbmRpIiwibXJ1bHpfbWFsYXlhbGFtIl0sInRvcmJveCI6ImFhMmIzYjY0LWEwZmEtNDZhNi1hYWI2LTZjYTMwZmI5ODIwNCJ9"
	EinthusanBase = "https://einthusan.asaddon.com/hindi,tamil,malayalam"
	BharatBingeBase = "https://bharat-binge.semi-column.workers.dev/platform%3Ajiohotstar-movies%2Cplatform%3Ajiohotstar-series%2Clang%3Ahi-recent-movie%2Clang%3Ahi-recent-series%2Clang%3Ata-recent-movie%2Clang%3Ata-recent-series%2Clang%3Aml-recent-movie%2Clang%3Aml-recent-series%2Clang%3Aml-top_rated-series%2Clang%3Ata-top_rated-series%2Clang%3Ahi-top_rated-series"
	MediaFusionBase = "https://mediafusion.elfhosted.com/D-cupbwkrkRy4bfr87sDOQAUwtxpaDC_ibNLar7_wIYGnnW3ubPah1ict9xkaDxgt-WQetZWhjCq_6NY7IDHZ-cJttrVLqqP7_0WKq9pE5G8ttdShUuJHlW2eCWayGqr7toPIEWHzXDZVsh2uEqbwFGcwJ4QQkxqRma8uFjUVuQSP6DV06t5ZjRdhy2An9mYKPXJSwfRQFKDuTNmhFt9w9CmEZEA8GDEYoUkYatf159_ZchY_k7wqE2-hf1WfrRuilQhdaSbSVZtfIYqrUuMaDKYWlTkl4PwuX9lX-x2R-CNsCnWzK4dEsKwGrgoystOeOFcOEEGg-7jtZJcbLcyLDxTscwkBV-6LWm42vJdJj7Rq7WNKvaXgA4f1Cr05SGnP9dZOw2NhQXDJWg_HwHn9DmtBSYUnh4PAsooqJxDwK3QhLfpZDml29EoW00HLGzvxu8Km-qFjmqZ_qseIIj6uMp7vtibIWTb53nTURh45QV_s2-v4KovFPh3CJ2i9ljfAqtRppqsOdSsb3KsupjLMLX5cY8QUoz4dGYlNq7lO2GNp1bfZqG8XHqTgeCqqagFkddaVF8SSDvinfCSxps5q2KGuvBSADOfttHkcjPP_g-VthkR8A44Q-nELu31oR9o0Ptm5hKZn7aGjSHdpan3oT2um5IpTMDSpjLnIQMd3X5GSx1g__6VlEXN-GfCwTljXa4Y4b0RV7qwu1MRQiyXSbPiNgKWjsiGoYe36O_k-NHJJi_41sNNYEjqUv4DVg1-lkaMILcIRfngehASN94eeeJvJuP1WIHIwqzPRrwNznMiFhpixrl_VVey5yez2En-Zeh1kYP8Ubny7J86N6zmb6mzSapG77A_hE1tGk4GMIKeqNjmaTaOKzmMqEs-QZvS7BtubFuI_qWX2ePJNy7DLxpwjQFGImJBFF-oipOOeaITbS12jSEJwuk_phu-OMLbWxSEPPZiiK36IMEYxeGQaOI9rXWhrkOnx8l2BDXzIafLSVtE-RkBzzaT9S7r2Zr-EeIWMVBSqyeMkK58JR05Ewp6AZUPNOfuLCs8q9r466tL2QaZ9GMK0fxf-EZZ4HCZzZ-efExCB0MXIQ-krardhq9-wHHb41SGkxA-Tk99gi7nOcxuboI34-MJNI9AWMbc9zy2rfe5q1uiDNdGDa0ZYA_LcYTDAac9J0wTUqglVvFBt-ElJUcA85tfzT-rknoMOQ9r9VGPbL6Y0-7ZedYBhYoPuqW-IrBfHrSNvtrnkhPuEx-dYIsFPj0j1KKrQDNvADiU3GDc2JqPOI20wzc6XMsDb3WSa7YUqcmjVtyg-X0nlvd8RNk8IFkmOKZxWQ7lgD"
)

var (
	ratingPrefixRegex = regexp.MustCompile(`^★\s*[\d\.]+\s*\|\s*`)
	yearRegex         = regexp.MustCompile(`^(.*?)\s*\((\d{4})\)`)
	alphaNumRegex     = regexp.MustCompile(`[^a-z0-9]`)
)

type stremioMeta struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Poster      string `json:"poster"`
	ReleaseInfo string `json:"releaseInfo"`
}

type stremioMetaResponse struct {
	Metas []stremioMeta `json:"metas"`
}

func fetchMetas(ctx context.Context, client *http.Client, targetURL string) ([]stremioMeta, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, targetURL)
	}
	var out stremioMetaResponse
	err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out)
	return out.Metas, err
}

func cleanTitle(title string) (string, int) {
	title = ratingPrefixRegex.ReplaceAllString(title, "")
	if matches := yearRegex.FindStringSubmatch(title); len(matches) > 2 {
		var y int
		fmt.Sscanf(matches[2], "%d", &y)
		return strings.TrimSpace(matches[1]), y
	}
	return strings.TrimSpace(title), 0
}

func normalize(s string) string {
	return alphaNumRegex.ReplaceAllString(strings.ToLower(s), "")
}

func searchCinemeta(ctx context.Context, client *http.Client, mediaType, title string) (string, string) {
	query := url.PathEscape(title)
	u := fmt.Sprintf("https://v3-cinemeta.strem.io/catalog/%s/top/search=%s.json", mediaType, query)
	metas, err := fetchMetas(ctx, client, u)
	if err != nil || len(metas) == 0 {
		return "", ""
	}
	target := normalize(title)
	for _, m := range metas {
		cand := normalize(m.Name)
		if cand == target || strings.Contains(cand, target) || strings.Contains(target, cand) {
			return m.ID, m.Poster
		}
	}
	return "", ""
}

func buildMovieUnion(ctx context.Context, client *http.Client, mrulzCat, einthusanCat string, maxItems int) []catalog.Item {
	mrulzMetas, _ := fetchMetas(ctx, client, fmt.Sprintf("%s/catalog/movie/%s.json", MovieRulzBase, mrulzCat))
	einthusanMetas, _ := fetchMetas(ctx, client, fmt.Sprintf("%s/catalog/movie/%s.json", EinthusanBase, einthusanCat))

	eMap := make(map[string]stremioMeta, len(einthusanMetas))
	for _, m := range einthusanMetas {
		eMap[normalize(m.Name)] = m
	}

	items := make([]catalog.Item, 0, maxItems)
	seenIDs := make(map[string]bool)
	seenNorms := make(map[string]bool)

	maxLen := len(mrulzMetas)
	if len(einthusanMetas) > maxLen {
		maxLen = len(einthusanMetas)
	}

	for i := 0; i < maxLen && len(items) < maxItems; i++ {
		// 1. MovieRulz item at index i
		if i < len(mrulzMetas) && len(items) < maxItems {
			m := mrulzMetas[i]
			rawName := m.Name
			title, year := cleanTitle(rawName)
			norm := normalize(title)

			imdbID := ""
			matchedTitle := title
			poster := m.Poster // preserve MovieRulz weserv webp poster!

			if eMatch, ok := eMap[norm]; ok {
				imdbID = eMatch.ID
				matchedTitle = eMatch.Name
				if poster == "" {
					poster = eMatch.Poster
				}
			} else {
				id, cPoster := searchCinemeta(ctx, client, "movie", title)
				if strings.HasPrefix(id, "tt") {
					imdbID = id
					if poster == "" {
						poster = cPoster
					}
				}
			}

			if strings.HasPrefix(imdbID, "tt") && !seenIDs[imdbID] && !seenNorms[norm] {
				seenIDs[imdbID] = true
				seenNorms[norm] = true
				if poster == "" {
					poster = fmt.Sprintf("https://images.metahub.space/poster/small/%s/img", imdbID)
				}
				items = append(items, catalog.Item{
					ID:     imdbID,
					Type:   "movie",
					Title:  matchedTitle,
					Year:   year,
					Poster: poster,
				})
			}
		}

		// 2. Einthusan item at index i
		if i < len(einthusanMetas) && len(items) < maxItems {
			e := einthusanMetas[i]
			if strings.HasPrefix(e.ID, "tt") {
				norm := normalize(e.Name)
				if !seenIDs[e.ID] && !seenNorms[norm] {
					seenIDs[e.ID] = true
					seenNorms[norm] = true
					poster := e.Poster
					if poster == "" {
						poster = fmt.Sprintf("https://images.metahub.space/poster/small/%s/img", e.ID)
					}
					items = append(items, catalog.Item{
						ID:     e.ID,
						Type:   "movie",
						Title:  e.Name,
						Poster: poster,
					})
				}
			}
		}
	}

	return items
}

func buildSeriesUnion(ctx context.Context, client *http.Client, mfCat, bbCat string, maxItems int) []catalog.Item {
	mfMetas, _ := fetchMetas(ctx, client, fmt.Sprintf("%s/catalog/series/%s.json", MediaFusionBase, mfCat))
	bbMetas, _ := fetchMetas(ctx, client, fmt.Sprintf("%s/catalog/series/%s.json", BharatBingeBase, bbCat))

	items := make([]catalog.Item, 0, maxItems)
	seenIDs := make(map[string]bool)
	seenNorms := make(map[string]bool)

	maxLen := len(bbMetas)
	if len(mfMetas) > maxLen {
		maxLen = len(mfMetas)
	}

	for i := 0; i < maxLen && len(items) < maxItems; i++ {
		// 1. Bharat Binge first (real regional Indian web series & serials)
		if i < len(bbMetas) && len(items) < maxItems {
			b := bbMetas[i]
			norm := normalize(b.Name)
			imdbID := b.ID
			poster := b.Poster

			if !strings.HasPrefix(imdbID, "tt") {
				id, cPoster := searchCinemeta(ctx, client, "series", b.Name)
				if strings.HasPrefix(id, "tt") {
					imdbID = id
					if poster == "" {
						poster = cPoster
					}
				}
			}

			if strings.HasPrefix(imdbID, "tt") && !seenIDs[imdbID] && !seenNorms[norm] {
				seenIDs[imdbID] = true
				seenNorms[norm] = true
				if poster == "" {
					poster = fmt.Sprintf("https://images.metahub.space/poster/small/%s/img", imdbID)
				}
				items = append(items, catalog.Item{
					ID:     imdbID,
					Type:   "series",
					Title:  b.Name,
					Poster: poster,
				})
			}
		}

		// 2. MediaFusion item at index i (Torbox-cached series)
		if i < len(mfMetas) && len(items) < maxItems {
			m := mfMetas[i]
			norm := normalize(m.Name)
			imdbID := m.ID
			poster := m.Poster

			if !strings.HasPrefix(imdbID, "tt") {
				id, cPoster := searchCinemeta(ctx, client, "series", m.Name)
				if strings.HasPrefix(id, "tt") {
					imdbID = id
					if poster == "" {
						poster = cPoster
					}
				}
			}

			if strings.HasPrefix(imdbID, "tt") && !seenIDs[imdbID] && !seenNorms[norm] {
				seenIDs[imdbID] = true
				seenNorms[norm] = true
				if poster == "" {
					poster = fmt.Sprintf("https://images.metahub.space/poster/small/%s/img", imdbID)
				}
				items = append(items, catalog.Item{
					ID:     imdbID,
					Type:   "series",
					Title:  m.Name,
					Poster: poster,
				})
			}
		}
	}

	return items
}

// SyncAll refreshes all 6 Indian catalog rows and syncs with Stremio.
func SyncAll(ctx context.Context, store catalog.Store, account stremio.AccountAPI, baseURL, feedToken string) error {
	client := &http.Client{Timeout: 10 * time.Second}

	// Build movie rows
	tamilMovies := buildMovieUnion(ctx, client, "mrulz_tamil", "tamil_board", 100)
	malayalamMovies := buildMovieUnion(ctx, client, "mrulz_malayalam", "malayalam_board", 100)
	hindiMovies := buildMovieUnion(ctx, client, "mrulz_hindi", "hindi_board", 100)

	// Build series rows
	tamilSeries := buildSeriesUnion(ctx, client, "tamil_series", "tmdb-ta-recent-series", 100)
	malayalamSeries := buildSeriesUnion(ctx, client, "malayalam_series", "tmdb-ml-recent-series", 100)
	hindiSeries := buildSeriesUnion(ctx, client, "hindi_series", "tmdb-hi-recent-series", 100)

	raw, err := store.Load(ctx)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}
	state, err := catalog.ParseState(raw)
	if err != nil {
		return fmt.Errorf("parsing state: %w", err)
	}

	rowMapping := map[string][]catalog.Item{
		"Latest Tamil":            tamilMovies,
		"Latest Malayalam":        malayalamMovies,
		"Latest Hindi":            hindiMovies,
		"Latest Tamil Series":     tamilSeries,
		"Latest Malayalam Series": malayalamSeries,
		"Latest Hindi Series":     hindiSeries,
	}

	for i := range state.Rows {
		if newItems, ok := rowMapping[state.Rows[i].Name]; ok && len(newItems) > 0 {
			state.Rows[i].Items = newItems
		}
	}

	state.Revision++
	updatedJSON, err := json.Marshal(state)
	if err != nil {
		return err
	}

	ok, err := store.CAS(ctx, raw, string(updatedJSON))
	if err != nil {
		return fmt.Errorf("saving state: %w", err)
	}
	if !ok {
		return fmt.Errorf("concurrent modification during state save")
	}

	// Sync to Stremio account
	if account.AuthKey != "" && baseURL != "" && feedToken != "" {
		manifestURL, err := stremio.ManifestURL(baseURL, feedToken, state.Revision)
		if err == nil {
			_ = account.Sync(ctx, state, manifestURL)
		}
	}

	return nil
}
