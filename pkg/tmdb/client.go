package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultTMDBBaseURL = "https://api.themoviedb.org/3"

// Client is a TMDB API client.
type Client struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
}

func NewClient(apiKey string) *Client {
	if apiKey == "" {
		apiKey = os.Getenv("TMDB_API_KEY")
	}
	return &Client{
		APIKey:  apiKey,
		BaseURL: defaultTMDBBaseURL,
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
}

type MovieResult struct {
	ID               int     `json:"id"`
	Title            string  `json:"title"`
	Name             string  `json:"name"`
	OriginalLanguage string  `json:"original_language"`
	Overview         string  `json:"overview"`
	PosterPath       string  `json:"poster_path"`
	ReleaseDate      string  `json:"release_date"`
	FirstAirDate     string  `json:"first_air_date"`
	VoteAverage      float64 `json:"vote_average"`
	GenreIDs         []int   `json:"genre_ids"`
	IMDbID           string  `json:"imdb_id,omitempty"`
}

type DiscoverPage struct {
	Page         int           `json:"page"`
	TotalResults int           `json:"total_results"`
	TotalPages   int           `json:"total_pages"`
	Results      []MovieResult `json:"results"`
}

type ExternalIDs struct {
	IMDbID string `json:"imdb_id"`
}

type DiscoverOptions struct {
	MediaType     string  `json:"media_type,omitempty"`     // movie or series (tv)
	Language      string  `json:"language,omitempty"`       // e.g. ml, ta, hi, te, ko, en
	Genre         string  `json:"genre,omitempty"`          // action, thriller, comedy, etc.
	WatchProvider string  `json:"watch_provider,omitempty"` // netflix, prime, hotstar
	Region        string  `json:"region,omitempty"`         // IN, US, etc.
	Year          int     `json:"year,omitempty"`
	MinRating     float64 `json:"min_rating,omitempty"`
	SortBy        string  `json:"sort_by,omitempty"`
	Limit         int     `json:"limit,omitempty"`
}

type SimilarOptions struct {
	IMDbID    string `json:"imdb_id,omitempty"`
	Title     string `json:"title,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type Item struct {
	IMDbID   string  `json:"imdb_id"`
	Title    string  `json:"title"`
	Type     string  `json:"type"`
	Year     int     `json:"year"`
	Rating   float64 `json:"rating"`
	Overview string  `json:"overview"`
	Poster   string  `json:"poster"`
	Language string  `json:"language,omitempty"`
}

func (c *Client) get(ctx context.Context, endpoint string, params url.Values, out any) error {
	if c.APIKey == "" {
		return fmt.Errorf("TMDB API key not configured. Set TMDB_API_KEY in environment or pass in request")
	}
	if params == nil {
		params = make(url.Values)
	}
	params.Set("api_key", c.APIKey)

	fullURL := fmt.Sprintf("%s%s?%s", c.BaseURL, endpoint, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return err
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("TMDB returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

// Map language aliases
func normalizeLang(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	switch l {
	case "malayalam":
		return "ml"
	case "tamil":
		return "ta"
	case "hindi":
		return "hi"
	case "telugu":
		return "te"
	case "korean":
		return "ko"
	case "japanese":
		return "ja"
	case "spanish":
		return "es"
	case "french":
		return "fr"
	case "german":
		return "de"
	case "english":
		return "en"
	default:
		return l
	}
}

// Map genre names to TMDB IDs
func genreID(genre string) int {
	g := strings.ToLower(strings.TrimSpace(genre))
	switch g {
	case "action":
		return 28
	case "adventure":
		return 12
	case "animation":
		return 16
	case "comedy":
		return 35
	case "crime":
		return 80
	case "documentary":
		return 99
	case "drama":
		return 18
	case "family":
		return 10751
	case "fantasy":
		return 14
	case "history":
		return 36
	case "horror":
		return 27
	case "music":
		return 10402
	case "mystery":
		return 9648
	case "romance":
		return 10749
	case "science fiction", "sci-fi":
		return 878
	case "thriller":
		return 53
	case "war":
		return 10752
	case "western":
		return 37
	default:
		if id, err := strconv.Atoi(g); err == nil {
			return id
		}
		return 0
	}
}

// Map watch providers to TMDB provider IDs
func providerID(provider, region string) (int, string) {
	p := strings.ToLower(strings.TrimSpace(provider))
	r := strings.ToUpper(strings.TrimSpace(region))
	if r == "" {
		r = "IN" // Default region for regional cinema
	}
	switch p {
	case "netflix":
		return 8, r
	case "prime", "amazon", "prime video":
		return 9, r
	case "hotstar", "disney", "disney+", "disney+ hotstar":
		if r == "IN" {
			return 122, r
		}
		return 337, r
	case "apple", "apple tv", "apple tv+":
		return 350, r
	case "hulu":
		return 15, "US"
	case "max", "hbo":
		return 1899, "US"
	default:
		if id, err := strconv.Atoi(p); err == nil {
			return id, r
		}
		return 0, r
	}
}

// GetIMDbID fetches the IMDb ID for a given TMDB movie or TV show.
func (c *Client) GetIMDbID(ctx context.Context, mediaType string, tmdbID int) (string, error) {
	endpoint := fmt.Sprintf("/movie/%d/external_ids", tmdbID)
	if mediaType == "series" || mediaType == "tv" {
		endpoint = fmt.Sprintf("/tv/%d/external_ids", tmdbID)
	}
	var ext ExternalIDs
	if err := c.get(ctx, endpoint, nil, &ext); err != nil {
		return "", err
	}
	return ext.IMDbID, nil
}

// FindByIMDbID converts an IMDb ID to TMDB ID and media type.
func (c *Client) FindByIMDbID(ctx context.Context, imdbID string) (int, string, error) {
	endpoint := fmt.Sprintf("/find/%s", imdbID)
	params := url.Values{"external_source": []string{"imdb_id"}}
	var resp struct {
		MovieResults []struct {
			ID int `json:"id"`
		} `json:"movie_results"`
		TVResults []struct {
			ID int `json:"tv_results"`
		} `json:"tv_results"`
	}
	if err := c.get(ctx, endpoint, params, &resp); err != nil {
		return 0, "", err
	}
	if len(resp.MovieResults) > 0 {
		return resp.MovieResults[0].ID, "movie", nil
	}
	if len(resp.TVResults) > 0 {
		return resp.TVResults[0].ID, "series", nil
	}
	return 0, "", fmt.Errorf("no TMDB entry found for IMDb ID %s", imdbID)
}

// Discover finds movies or TV shows based on regional language, genres, providers, etc.
func (c *Client) Discover(ctx context.Context, opts DiscoverOptions) ([]Item, error) {
	if c.APIKey == "" {
		return c.discoverCinemeta(ctx, opts)
	}

	endpoint := "/discover/movie"
	isTV := opts.MediaType == "series" || opts.MediaType == "tv"
	if isTV {
		endpoint = "/discover/tv"
	}

	params := make(url.Values)
	if opts.Language != "" {
		lang := normalizeLang(opts.Language)
		params.Set("with_original_language", lang)
	}
	if opts.Genre != "" {
		if gid := genreID(opts.Genre); gid > 0 {
			params.Set("with_genres", strconv.Itoa(gid))
		}
	}
	if opts.WatchProvider != "" {
		pid, reg := providerID(opts.WatchProvider, opts.Region)
		if pid > 0 {
			params.Set("with_watch_providers", strconv.Itoa(pid))
			params.Set("watch_region", reg)
		}
	} else if opts.Region != "" {
		params.Set("region", opts.Region)
	}

	if opts.Year > 0 {
		if isTV {
			params.Set("first_air_date_year", strconv.Itoa(opts.Year))
		} else {
			params.Set("primary_release_year", strconv.Itoa(opts.Year))
		}
	}
	if opts.MinRating > 0 {
		params.Set("vote_average.gte", fmt.Sprintf("%.1f", opts.MinRating))
		params.Set("vote_count.gte", "10") // Avoid noise with 1 vote
	}
	if opts.SortBy != "" {
		params.Set("sort_by", opts.SortBy)
	} else {
		params.Set("sort_by", "popularity.desc")
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	var page DiscoverPage
	if err := c.get(ctx, endpoint, params, &page); err != nil {
		return nil, err
	}

	allResults := page.Results
	pagesNeeded := (limit + 19) / 20
	if pagesNeeded > 1 && page.TotalPages > 1 {
		if pagesNeeded > page.TotalPages {
			pagesNeeded = page.TotalPages
		}
		type pageRes struct {
			page int
			res  []MovieResult
		}
		ch := make(chan pageRes, pagesNeeded-1)
		var pageWg sync.WaitGroup
		for p := 2; p <= pagesNeeded; p++ {
			pageWg.Add(1)
			go func(pageNum int) {
				defer pageWg.Done()
				pageParams := make(url.Values)
				for k, v := range params {
					pageParams[k] = v
				}
				pageParams.Set("page", strconv.Itoa(pageNum))
				var pData DiscoverPage
				if err := c.get(ctx, endpoint, pageParams, &pData); err == nil {
					ch <- pageRes{page: pageNum, res: pData.Results}
				}
			}(p)
		}
		pageWg.Wait()
		close(ch)

		pageMap := make(map[int][]MovieResult)
		for pr := range ch {
			pageMap[pr.page] = pr.res
		}
		for p := 2; p <= pagesNeeded; p++ {
			if list, ok := pageMap[p]; ok {
				allResults = append(allResults, list...)
			}
		}
	}

	if len(allResults) > limit {
		allResults = allResults[:limit]
	}

	return c.enrichWithIMDb(ctx, isTV, allResults)
}

// Similar finds titles similar or recommended for an IMDb ID or Title.
func (c *Client) Similar(ctx context.Context, opts SimilarOptions) ([]Item, error) {
	if c.APIKey == "" {
		return c.similarCinemeta(ctx, opts)
	}

	isTV := opts.MediaType == "series" || opts.MediaType == "tv"
	mediaTypeStr := "movie"
	if isTV {
		mediaTypeStr = "tv"
	}

	var tmdbID int
	if opts.IMDbID != "" && strings.HasPrefix(opts.IMDbID, "tt") {
		id, detectedType, err := c.FindByIMDbID(ctx, opts.IMDbID)
		if err != nil {
			return nil, err
		}
		tmdbID = id
		if detectedType == "series" {
			isTV = true
			mediaTypeStr = "tv"
		}
	} else if opts.Title != "" {
		// Search by title
		searchEndpoint := "/search/" + mediaTypeStr
		params := url.Values{"query": []string{opts.Title}}
		var page DiscoverPage
		if err := c.get(ctx, searchEndpoint, params, &page); err != nil {
			return nil, err
		}
		if len(page.Results) == 0 {
			return nil, fmt.Errorf("no titles found matching %q", opts.Title)
		}
		tmdbID = page.Results[0].ID
	} else {
		return nil, fmt.Errorf("either imdb_id or title is required")
	}

	// Fetch recommendations first; if empty, fetch similar
	endpoint := fmt.Sprintf("/%s/%d/recommendations", mediaTypeStr, tmdbID)
	var page DiscoverPage
	_ = c.get(ctx, endpoint, nil, &page)
	if len(page.Results) == 0 {
		simEndpoint := fmt.Sprintf("/%s/%d/similar", mediaTypeStr, tmdbID)
		if err := c.get(ctx, simEndpoint, nil, &page); err != nil {
			return nil, err
		}
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	if len(page.Results) > limit {
		page.Results = page.Results[:limit]
	}

	return c.enrichWithIMDb(ctx, isTV, page.Results)
}

// enrichWithIMDb concurrently resolves IMDb IDs and builds Item models.
func (c *Client) enrichWithIMDb(ctx context.Context, isTV bool, results []MovieResult) ([]Item, error) {
	type enrichResult struct {
		index int
		item  Item
	}

	resChan := make(chan enrichResult, len(results))
	var wg sync.WaitGroup

	mediaType := "movie"
	if isTV {
		mediaType = "series"
	}

	for i, res := range results {
		wg.Add(1)
		go func(idx int, r MovieResult) {
			defer wg.Done()
			title := r.Title
			if title == "" {
				title = r.Name
			}
			date := r.ReleaseDate
			if date == "" {
				date = r.FirstAirDate
			}
			year := 0
			if len(date) >= 4 {
				year, _ = strconv.Atoi(date[:4])
			}

			poster := ""
			if r.PosterPath != "" {
				poster = "https://image.tmdb.org/t/p/w500" + r.PosterPath
			}

			// Fetch external IMDb ID
			imdbID, _ := c.GetIMDbID(ctx, mediaType, r.ID)

			resChan <- enrichResult{
				index: idx,
				item: Item{
					IMDbID:   imdbID,
					Title:    title,
					Type:     mediaType,
					Year:     year,
					Rating:   r.VoteAverage,
					Overview: r.Overview,
					Poster:   poster,
					Language: r.OriginalLanguage,
				},
			}
		}(i, res)
	}

	wg.Wait()
	close(resChan)

	ordered := make([]Item, len(results))
	for res := range resChan {
		ordered[res.index] = res.item
	}

	var valid []Item
	for _, it := range ordered {
		// Only include items where IMDb ID was successfully resolved
		if it.IMDbID != "" && strings.HasPrefix(it.IMDbID, "tt") {
			valid = append(valid, it)
		}
	}

	return valid, nil
}

func (c *Client) discoverCinemeta(ctx context.Context, opts DiscoverOptions) ([]Item, error) {
	mediaType := opts.MediaType
	if mediaType == "" {
		mediaType = "movie"
	}
	if mediaType == "tv" {
		mediaType = "series"
	}

	u := fmt.Sprintf("https://v3-cinemeta.strem.io/catalog/%s/top.json", mediaType)
	if opts.Genre != "" {
		u = fmt.Sprintf("https://v3-cinemeta.strem.io/catalog/%s/top/genre=%s.json", mediaType, url.PathEscape(opts.Genre))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var data struct {
		Metas []struct {
			ID          string `json:"id"`
			IMDbID      string `json:"imdb_id"`
			Name        string `json:"name"`
			Type        string `json:"type"`
			Poster      string `json:"poster"`
			ReleaseInfo string `json:"releaseInfo"`
			Description string `json:"description"`
			IMDbRating  string `json:"imdbRating"`
		} `json:"metas"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	limit := opts.Limit
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	var items []Item
	for _, m := range data.Metas {
		id := m.IMDbID
		if id == "" {
			id = m.ID
		}
		if !strings.HasPrefix(id, "tt") {
			continue
		}
		year := 0
		if len(m.ReleaseInfo) >= 4 {
			year, _ = strconv.Atoi(m.ReleaseInfo[:4])
		}
		if opts.Year > 0 && year > 0 && year != opts.Year {
			continue
		}

		rating, _ := strconv.ParseFloat(m.IMDbRating, 64)
		if opts.MinRating > 0 && rating > 0 && rating < opts.MinRating {
			continue
		}

		mType := m.Type
		if mType == "" {
			mType = mediaType
		}

		items = append(items, Item{
			IMDbID:   id,
			Title:    m.Name,
			Type:     mType,
			Year:     year,
			Rating:   rating,
			Overview: m.Description,
			Poster:   m.Poster,
		})

		if len(items) >= limit {
			break
		}
	}

	return items, nil
}

func (c *Client) similarCinemeta(ctx context.Context, opts SimilarOptions) ([]Item, error) {
	mediaType := opts.MediaType
	if mediaType == "" {
		mediaType = "movie"
	}
	if mediaType == "tv" {
		mediaType = "series"
	}

	genre := "Drama"
	query := opts.Title
	if query == "" && opts.IMDbID != "" {
		metaURL := fmt.Sprintf("https://v3-cinemeta.strem.io/meta/%s/%s.json", mediaType, opts.IMDbID)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
		if resp, err := c.HTTP.Do(req); err == nil && resp.StatusCode == http.StatusOK {
			var res struct {
				Meta struct {
					Genres []string `json:"genres"`
				} `json:"meta"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&res)
			_ = resp.Body.Close()
			if len(res.Meta.Genres) > 0 {
				genre = res.Meta.Genres[0]
			}
		}
	} else if query != "" {
		searchURL := fmt.Sprintf("https://v3-cinemeta.strem.io/catalog/%s/top/search=%s.json", mediaType, url.PathEscape(query))
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
		if resp, err := c.HTTP.Do(req); err == nil && resp.StatusCode == http.StatusOK {
			var res struct {
				Metas []struct {
					Genres []string `json:"genres"`
				} `json:"metas"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&res)
			_ = resp.Body.Close()
			if len(res.Metas) > 0 && len(res.Metas[0].Genres) > 0 {
				genre = res.Metas[0].Genres[0]
			}
		}
	}

	return c.discoverCinemeta(ctx, DiscoverOptions{
		MediaType: mediaType,
		Genre:     genre,
		Limit:     opts.Limit,
	})
}
