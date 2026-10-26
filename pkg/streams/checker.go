package streams

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var seederRegex = regexp.MustCompile(`(?:👤|seeders?:\s*|peers?:\s*)(\d+)`)

type StreamAvailabilityResult struct {
	IMDbID         string `json:"imdb_id"`
	Available      bool   `json:"available"`
	StreamCount    int    `json:"stream_count"`
	HighestQuality string `json:"highest_quality"`
	MaxSeeders     int    `json:"max_seeders"`
	BestStream     string `json:"best_stream,omitempty"`
	TorboxCached   bool   `json:"torbox_cached,omitempty"`
	Message        string `json:"message"`
}

type torrentioStream struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	InfoHash string `json:"infoHash"`
}

type torrentioResponse struct {
	Streams []torrentioStream `json:"streams"`
}

// CheckAvailability queries Torrentio and optional TorBox cache to check if a title has healthy streams.
func CheckAvailability(ctx context.Context, mediaType, imdbID, torboxKey, streamBase string) (StreamAvailabilityResult, error) {
	if mediaType == "" {
		mediaType = "movie"
	}
	if !strings.HasPrefix(imdbID, "tt") {
		return StreamAvailabilityResult{}, fmt.Errorf("invalid IMDb ID: %s", imdbID)
	}

	url := fmt.Sprintf("https://torrentio.strem.fun/stream/%s/%s.json", mediaType, imdbID)
	if streamBase != "" {
		url = fmt.Sprintf("%s/stream/%s/%s.json", strings.TrimRight(streamBase, "/"), mediaType, imdbID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return StreamAvailabilityResult{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return StreamAvailabilityResult{}, fmt.Errorf("stream check failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return StreamAvailabilityResult{
			IMDbID:    imdbID,
			Available: false,
			Message:   fmt.Sprintf("Stream provider returned HTTP %d", resp.StatusCode),
		}, nil
	}

	var data torrentioResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&data); err != nil {
		return StreamAvailabilityResult{}, fmt.Errorf("invalid stream response: %w", err)
	}

	if len(data.Streams) == 0 {
		return StreamAvailabilityResult{
			IMDbID:    imdbID,
			Available: false,
			Message:   "No active torrent streams found for this title.",
		}, nil
	}

	highestQuality := "SD"
	maxSeeders := 0
	bestTitle := ""
	torboxCached := false
	var hashes []string

	for _, s := range data.Streams {
		content := strings.ToLower(s.Name + " " + s.Title)
		if strings.Contains(s.Name, "[TB+]") || strings.Contains(s.Name, "[RD+]") || strings.Contains(s.Name, "[AD+]") {
			torboxCached = true
		}
		if strings.Contains(content, "4k") || strings.Contains(content, "2160p") {
			highestQuality = "4K"
		} else if highestQuality != "4K" && (strings.Contains(content, "1080p") || strings.Contains(content, "fhd")) {
			highestQuality = "1080p"
		} else if highestQuality != "4K" && highestQuality != "1080p" && (strings.Contains(content, "720p") || strings.Contains(content, "hd")) {
			highestQuality = "720p"
		}

		if match := seederRegex.FindStringSubmatch(s.Title); len(match) > 1 {
			if count, err := strconv.Atoi(match[1]); err == nil && count > maxSeeders {
				maxSeeders = count
			}
		}

		if bestTitle == "" && s.Title != "" {
			firstLine := strings.Split(s.Title, "\n")[0]
			bestTitle = strings.TrimSpace(firstLine)
		}

		if s.InfoHash != "" && len(hashes) < 15 {
			hashes = append(hashes, s.InfoHash)
		}
	}

	if !torboxCached && torboxKey != "" && len(hashes) > 0 {
		torboxCached = checkTorboxCache(ctx, client, torboxKey, hashes)
	}

	cachedMsg := ""
	if torboxCached {
		cachedMsg = " (Instantly cached on TorBox!)"
	}

	return StreamAvailabilityResult{
		IMDbID:         imdbID,
		Available:      true,
		StreamCount:    len(data.Streams),
		HighestQuality: highestQuality,
		MaxSeeders:     maxSeeders,
		BestStream:     bestTitle,
		TorboxCached:   torboxCached,
		Message:        fmt.Sprintf("Found %d streams up to %s with max %d seeders.%s", len(data.Streams), highestQuality, maxSeeders, cachedMsg),
	}, nil
}

func checkTorboxCache(ctx context.Context, client *http.Client, apiKey string, hashes []string) bool {
	hashParam := strings.Join(hashes, ",")
	endpoint := fmt.Sprintf("https://api.torbox.app/v1/api/torrents/checkcached?hash=%s&format=object", hashParam)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return false
	}
	defer resp.Body.Close()

	var result struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || !result.Success {
		return false
	}

	// If format=object, Data is a map of cached hashes
	var cachedMap map[string]any
	if err := json.Unmarshal(result.Data, &cachedMap); err == nil {
		return len(cachedMap) > 0
	}
	return false
}
