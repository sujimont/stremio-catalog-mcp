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
	"sync"
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
	ratingPrefixRegex  = regexp.MustCompile(`^★\s*[\d\.]+\s*\|\s*`)
	yearRegex          = regexp.MustCompile(`^(.*?)\s*\((\d{4})\)`)
	alphaNumRegex      = regexp.MustCompile(`[^a-z0-9]`)
	foreignDubRegex    = regexp.MustCompile(`(?i)\b(hollywood|english\s*dubbed|foreign)\b`)
	exactForeignRegex  = regexp.MustCompile(`(?i)^(cross|elle|dark|days|youth|doctors|resort)(\s*\(.*\))?$`)
	foreignFranchises  = []string{
		// Anime & Animation
		"spongebob", "naruto", "jujutsu kaisen", "one piece", "blue box", "shin-chan",
		"crayon shin-chan", "one punch man", "solo leveling", "bleach", "attack on titan",
		"demon slayer", "dragon ball", "death note", "chainsaw man", "avatar", "we bare bears",
		"he-man", "thunder 3", "to be hero", "arcane",
		// Marvel / DC / Superhero
		"spider-noir", "spider noir", "spider-man", "spiderman", "batman", "superman", "ironheart",
		"moon knight", "she-hulk", "hawkeye", "the flash", "loki", "ms marvel", "ms. marvel",
		"deadpool", "dc's legends", "legends of tomorrow", "peacemaker", "the boys", "invincible",
		// Star Wars & Sci-Fi / Fantasy
		"star wars", "mandalorian", "boba fett", "obi-wan", "lord of the rings", "rings of power",
		"fallout", "dune: prophecy", "halo", "resident evil", "transformers", "locke and key",
		"lost in space", "la brea", "the witcher", "house of the dragon", "game of thrones", "stranger things",
		// Western drama / thriller / crime / reality
		"lupin", "the gentlemen", "the terminal list", "mr. & mrs. smith", "hunting party",
		"good casting", "taken: natalee holloway", "has fallen", "clean up company",
		"mystery of edwin drood", "man who fell to earth", "every year after",
		"severance", "the penguin", "shogun", "ted lasso", "succession", "the last of us",
		"money heist", "reacher", "dexter", "prison break", "breaking bad",
		"better call saul", "sherlock", "black mirror", "wednesday", "umbrella academy",
		"agatha christie", "beast games", "beauty in black", "citadel: diana", "family matters",
		"heels", "high school", "it welcome to derry", "jazz city", "lucifer", "man vs baby",
		"missing you", "only murders in the building", "outlast", "part-timers", "real kashmir football club",
		"rise and fall", "second love", "secret stories: roslin", "single's inferno", "suits: l.a.",
		"surely tomorrow", "the asset", "the endgame", "the game: you never play alone",
		"the ordinary jackpot", "the summer i turned pretty", "the traitors", "the watch",
		"two graves", "van der valk", "we were liars", "scrubs", "little house on the prairie",
		"visionquest", "falcon and the winter soldier", "winter soldier",
		// Asian (K-Drama / C-Drama) dubbed
		"when life gives you tangerines", "my lovely liar", "reincarnated aristocrat",
		"squid game", "all of us are dead", "alice in borderland", "parasyte", "hellbound",
		"sweet home", "vincenzo", "crash landing", "queen of tears", "dr. romantic",
		"descendants of the sun", "goblin", "itaewon class", "the glory",
		"death's game", "daily dose of sunshine", "twinkling watermelon",
		"bloodhounds", "see you in my 19th life",
	}
)

func isForeign(name string) bool {
	if foreignDubRegex.MatchString(name) {
		return true
	}
	trimmed := strings.TrimSpace(name)
	if exactForeignRegex.MatchString(trimmed) {
		return true
	}
	low := strings.ToLower(trimmed)
	for _, f := range foreignFranchises {
		if strings.Contains(low, f) {
			return true
		}
	}
	return false
}

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
	s = strings.ReplaceAll(s, "&", "and")
	return alphaNumRegex.ReplaceAllString(strings.ToLower(s), "")
}

func searchCinemeta(ctx context.Context, client *http.Client, mediaType, title string) (string, string) {
	ctx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()

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
	if len(metas) > 0 {
		first := metas[0]
		cand := normalize(first.Name)
		if len(cand) >= 4 && len(target) >= 4 && (strings.HasPrefix(cand, target) || strings.HasPrefix(target, cand)) {
			return first.ID, first.Poster
		}
	}
	return "", ""
}

func buildMovieUnion(ctx context.Context, client *http.Client, mrulzMetas, einthusanMetas []stremioMeta, maxItems int) []catalog.Item {
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
			if isForeign(rawName) {
				continue
			}
			title, year := cleanTitle(rawName)
			if isForeign(title) {
				continue
			}
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

			// Fallback to MovieRulz ID if Cinemeta didn't have an IMDb ID
			if imdbID == "" && m.ID != "" {
				imdbID = m.ID
			}

			if imdbID != "" && !seenIDs[imdbID] && !seenNorms[norm] {
				seenIDs[imdbID] = true
				seenNorms[norm] = true
				if poster == "" && strings.HasPrefix(imdbID, "tt") {
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
			if strings.HasPrefix(e.ID, "tt") && !isForeign(e.Name) {
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

func buildSeriesUnion(recentMetas, mfMetas, topRatedMetas []stremioMeta, maxItems int) []catalog.Item {
	items := make([]catalog.Item, 0, maxItems)
	seenIDs := make(map[string]bool)
	seenNorms := make(map[string]bool)

	addSeries := func(m stremioMeta) {
		if !strings.HasPrefix(m.ID, "tt") {
			return
		}
		if isForeign(m.Name) {
			return
		}
		norm := normalize(m.Name)
		if seenIDs[m.ID] || seenNorms[norm] {
			return
		}
		seenIDs[m.ID] = true
		seenNorms[norm] = true
		poster := m.Poster
		if poster == "" {
			poster = fmt.Sprintf("https://images.metahub.space/poster/small/%s/img", m.ID)
		}
		items = append(items, catalog.Item{
			ID:     m.ID,
			Type:   "series",
			Title:  m.Name,
			Poster: poster,
		})
	}

	maxLen := len(recentMetas)
	if len(mfMetas) > maxLen {
		maxLen = len(mfMetas)
	}
	if len(topRatedMetas) > maxLen {
		maxLen = len(topRatedMetas)
	}

	for i := 0; i < maxLen && len(items) < maxItems; i++ {
		// 1. Bharat Binge Recent releases
		if i < len(recentMetas) && len(items) < maxItems {
			addSeries(recentMetas[i])
		}
		// 2. MediaFusion Indian regional & multi-audio releases
		if i < len(mfMetas) && len(items) < maxItems {
			addSeries(mfMetas[i])
		}
		// 3. Bharat Binge Top Rated serials
		if i < len(topRatedMetas) && len(items) < maxItems {
			addSeries(topRatedMetas[i])
		}
	}

	return items
}

// SyncAll refreshes all 6 Indian catalog rows and syncs with Stremio.
func SyncAll(ctx context.Context, store catalog.Store, account stremio.AccountAPI, baseURL, feedToken string) error {
	client := &http.Client{Timeout: 35 * time.Second}

	// Ping MovieRulz base to wake Render instance from sleep
	go func() {
		wakeReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://movierulz-stremio.onrender.com/manifest.json", nil)
		if err == nil {
			wakeReq.Header.Set("User-Agent", "Mozilla/5.0")
			if resp, err := client.Do(wakeReq); err == nil {
				resp.Body.Close()
			}
		}
	}()

	var (
		mrulzTa, mrulzMl, mrulzHi []stremioMeta
		einTa, einMl, einHi       []stremioMeta
		bbTaRecent, bbTaTop       []stremioMeta
		bbMlRecent, bbMlTop       []stremioMeta
		bbHiRecent, bbHiTop       []stremioMeta
		mfTa, mfMl, mfHi          []stremioMeta
		wg                        sync.WaitGroup
	)

	fetchWithRetry := func(target *[]stremioMeta, targetURL string, retries int) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempt := 0; attempt <= retries; attempt++ {
				metas, err := fetchMetas(ctx, client, targetURL)
				if err == nil && len(metas) > 0 {
					*target = metas
					return
				}
				if attempt < retries {
					select {
					case <-ctx.Done():
						return
					case <-time.After(1 * time.Second):
					}
				}
			}
		}()
	}

	fetchWithRetry(&mrulzTa, fmt.Sprintf("%s/catalog/movie/mrulz_tamil.json", MovieRulzBase), 1)
	fetchWithRetry(&mrulzMl, fmt.Sprintf("%s/catalog/movie/mrulz_malayalam.json", MovieRulzBase), 1)
	fetchWithRetry(&mrulzHi, fmt.Sprintf("%s/catalog/movie/mrulz_hindi.json", MovieRulzBase), 1)

	fetchWithRetry(&einTa, fmt.Sprintf("%s/catalog/movie/tamil_board.json", EinthusanBase), 0)
	fetchWithRetry(&einMl, fmt.Sprintf("%s/catalog/movie/malayalam_board.json", EinthusanBase), 0)
	fetchWithRetry(&einHi, fmt.Sprintf("%s/catalog/movie/hindi_board.json", EinthusanBase), 0)

	fetchWithRetry(&bbTaRecent, fmt.Sprintf("%s/catalog/series/tmdb-ta-recent-series.json", BharatBingeBase), 0)
	fetchWithRetry(&bbTaTop, fmt.Sprintf("%s/catalog/series/tmdb-ta-top_rated-series.json", BharatBingeBase), 0)

	fetchWithRetry(&bbMlRecent, fmt.Sprintf("%s/catalog/series/tmdb-ml-recent-series.json", BharatBingeBase), 0)
	fetchWithRetry(&bbMlTop, fmt.Sprintf("%s/catalog/series/tmdb-ml-top_rated-series.json", BharatBingeBase), 0)

	fetchWithRetry(&bbHiRecent, fmt.Sprintf("%s/catalog/series/tmdb-hi-recent-series.json", BharatBingeBase), 0)
	fetchWithRetry(&bbHiTop, fmt.Sprintf("%s/catalog/series/tmdb-hi-top_rated-series.json", BharatBingeBase), 0)

	fetchWithRetry(&mfTa, fmt.Sprintf("%s/catalog/series/tamil_series.json", MediaFusionBase), 0)
	fetchWithRetry(&mfMl, fmt.Sprintf("%s/catalog/series/malayalam_series.json", MediaFusionBase), 0)
	fetchWithRetry(&mfHi, fmt.Sprintf("%s/catalog/series/hindi_series.json", MediaFusionBase), 0)

	wg.Wait()

	raw, err := store.Load(ctx)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}
	state, err := catalog.ParseState(raw)
	if err != nil {
		return fmt.Errorf("parsing state: %w", err)
	}

	// Safeguard: If MovieRulz failed to respond, salvage previously cached MovieRulz items from state
	salvagePreviousMrulz := func(rowName string) []stremioMeta {
		for _, row := range state.Rows {
			if row.Name == rowName {
				out := make([]stremioMeta, 0, len(row.Items))
				for _, it := range row.Items {
					out = append(out, stremioMeta{
						ID:     it.ID,
						Type:   it.Type,
						Name:   it.Title,
						Poster: it.Poster,
					})
				}
				return out
			}
		}
		return nil
	}

	if len(mrulzTa) == 0 {
		mrulzTa = salvagePreviousMrulz("Latest Tamil")
	}
	if len(mrulzMl) == 0 {
		mrulzMl = salvagePreviousMrulz("Latest Malayalam")
	}
	if len(mrulzHi) == 0 {
		mrulzHi = salvagePreviousMrulz("Latest Hindi")
	}

	// Build movie rows with interleaving
	tamilMovies := buildMovieUnion(ctx, client, mrulzTa, einTa, 100)
	malayalamMovies := buildMovieUnion(ctx, client, mrulzMl, einMl, 100)
	hindiMovies := buildMovieUnion(ctx, client, mrulzHi, einHi, 100)

	// Build series rows with interleaving (Bharat Binge recent + MediaFusion multi-audio/Indian + Bharat Binge top-rated)
	tamilSeries := buildSeriesUnion(bbTaRecent, mfTa, bbTaTop, 100)
	malayalamSeries := buildSeriesUnion(bbMlRecent, mfMl, bbMlTop, 100)
	hindiSeries := buildSeriesUnion(bbHiRecent, mfHi, bbHiTop, 100)

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
