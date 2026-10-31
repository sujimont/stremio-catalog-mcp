package torbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultTorboxBaseURL = "https://api.torbox.app/v1/api"

type Client struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
}

func NewClient(apiKey string) *Client {
	if apiKey == "" {
		apiKey = os.Getenv("TORBOX_API_KEY")
	}
	return &Client{
		APIKey:  apiKey,
		BaseURL: defaultTorboxBaseURL,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

type TorrentCreateResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Data    struct {
		TorrentID int    `json:"torrent_id"`
		Hash      string `json:"hash"`
		Name      string `json:"name"`
	} `json:"data"`
}

type WebDLCreateResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Data    struct {
		DownloadID int    `json:"download_id"`
		URL        string `json:"url"`
		Name       string `json:"name"`
	} `json:"data"`
}

type CheckCachedResponse struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
}

type TorrentFile struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	ShortName    string `json:"short_name,omitempty"`
	AbsolutePath string `json:"absolute_path,omitempty"`
}

type TorrentItem struct {
	ID            int           `json:"id"`
	Hash          string        `json:"hash"`
	Name          string        `json:"name"`
	Size          int64         `json:"size"`
	Active        bool          `json:"active"`
	DownloadState string        `json:"download_state"`
	Progress      float64       `json:"progress"`
	Files         []TorrentFile `json:"files,omitempty"`
	DownloadSpeed int64         `json:"download_speed,omitempty"`
	UploadSpeed   int64         `json:"upload_speed,omitempty"`
	CreatedAt     string        `json:"created_at,omitempty"`
	UpdatedAt     string        `json:"updated_at,omitempty"`
	Magnet        string        `json:"magnet,omitempty"`
	IsCached      bool          `json:"is_cached,omitempty"`
}

type WebDLItem struct {
	ID            int           `json:"id"`
	Name          string        `json:"name"`
	Size          int64         `json:"size"`
	Active        bool          `json:"active"`
	DownloadState string        `json:"download_state"`
	Progress      float64       `json:"progress"`
	DownloadURL   string        `json:"download_url,omitempty"`
	Files         []TorrentFile `json:"files,omitempty"`
	CreatedAt     string        `json:"created_at,omitempty"`
	UpdatedAt     string        `json:"updated_at,omitempty"`
}

type UploadItem struct {
	ID            int           `json:"id"`
	Type          string        `json:"type"` // "torrent" or "webdl"
	Name          string        `json:"name"`
	Size          int64         `json:"size"`
	SizeFormatted string        `json:"size_formatted"`
	Status        string        `json:"status"` // "completed", "downloading", "uploading", etc.
	Progress      float64       `json:"progress"`
	Hash          string        `json:"hash,omitempty"`
	Files         []TorrentFile `json:"files,omitempty"`
	CreatedAt     string        `json:"created_at,omitempty"`
	UpdatedAt     string        `json:"updated_at,omitempty"`
	StreamURL     string        `json:"stream_url,omitempty"`
}

func (u UploadItem) StremioID() string {
	return fmt.Sprintf("tb:%s:%d", u.Type, u.ID)
}

type TorboxListResponse struct {
	Success bool            `json:"success"`
	Detail  string          `json:"detail,omitempty"`
	Error   string          `json:"error,omitempty"`
	Data    json.RawMessage `json:"data"`
}

// CheckCached checks if any of the provided info hashes are already cached on TorBox.
func (c *Client) CheckCached(ctx context.Context, hashes []string) (map[string]bool, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("TORBOX_API_KEY is not configured")
	}
	if len(hashes) == 0 {
		return map[string]bool{}, nil
	}

	hashParam := strings.Join(hashes, ",")
	endpoint := fmt.Sprintf("%s/torrents/checkcached?hash=%s&format=object", c.BaseURL, url.QueryEscape(hashParam))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TorBox checkcached returned HTTP %d", resp.StatusCode)
	}

	var res CheckCachedResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	result := make(map[string]bool)
	var objMap map[string]any
	if err := json.Unmarshal(res.Data, &objMap); err == nil {
		for h := range objMap {
			result[strings.ToLower(h)] = true
		}
	}

	return result, nil
}

// AddTorrent adds a magnet link or torrent to TorBox.
func (c *Client) AddTorrent(ctx context.Context, magnet string) (*TorrentCreateResponse, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("TORBOX_API_KEY is not configured")
	}

	endpoint := fmt.Sprintf("%s/torrents/createtorrent", c.BaseURL)

	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	if err := w.WriteField("magnet", magnet); err != nil {
		return nil, err
	}
	if err := w.WriteField("seed", "1"); err != nil {
		return nil, err
	}
	if err := w.WriteField("allow_zip", "false"); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &b)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var res TorrentCreateResponse
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return nil, fmt.Errorf("failed to parse TorBox response: %w (body: %s)", err, string(bodyBytes))
	}

	if !res.Success {
		msg := res.Detail
		if msg == "" {
			msg = res.Error
		}
		return nil, fmt.Errorf("TorBox add torrent failed: %s", msg)
	}

	return &res, nil
}

// CreateTorrent is an alias for AddTorrent matching client test interface.
func (c *Client) CreateTorrent(ctx context.Context, magnet string, _ any) (*TorrentCreateResponse, error) {
	return c.AddTorrent(ctx, magnet)
}

// AddWebDownload adds a direct HTTP / HTTPS link to TorBox WebDL engine.
func (c *Client) AddWebDownload(ctx context.Context, link, name string) (*WebDLCreateResponse, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("TORBOX_API_KEY is not configured")
	}

	endpoint := fmt.Sprintf("%s/webdl/createwebdownload", c.BaseURL)

	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	if err := w.WriteField("link", link); err != nil {
		return nil, err
	}
	if name != "" {
		if err := w.WriteField("name", name); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &b)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var res WebDLCreateResponse
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return nil, fmt.Errorf("failed to parse TorBox response: %w (body: %s)", err, string(bodyBytes))
	}

	if !res.Success {
		msg := res.Detail
		if msg == "" {
			msg = res.Error
		}
		return nil, fmt.Errorf("TorBox add WebDL failed: %s", msg)
	}

	return &res, nil
}

// CreateWebDL is an alias for AddWebDownload matching client test interface.
func (c *Client) CreateWebDL(ctx context.Context, link, name string) (*WebDLCreateResponse, error) {
	return c.AddWebDownload(ctx, link, name)
}

// GetTorrents returns the list of torrents in the user's TorBox account.
func (c *Client) GetTorrents(ctx context.Context, bypassCache bool) ([]TorrentItem, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("TORBOX_API_KEY is not configured")
	}

	endpoint := fmt.Sprintf("%s/torrents/mylist", c.BaseURL)
	if bypassCache {
		endpoint += "?bypass_cache=true"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TorBox mylist returned HTTP %d", resp.StatusCode)
	}

	var res TorboxListResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("failed to decode TorBox response: %w", err)
	}

	if !res.Success {
		msg := res.Detail
		if msg == "" {
			msg = res.Error
		}
		return nil, fmt.Errorf("TorBox get torrents failed: %s", msg)
	}

	trimmed := bytes.TrimSpace(res.Data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return []TorrentItem{}, nil
	}

	var list []TorrentItem
	if bytes.HasPrefix(trimmed, []byte("[")) {
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return nil, fmt.Errorf("failed to unmarshal torrents array: %w", err)
		}
	} else if bytes.HasPrefix(trimmed, []byte("{")) {
		var single TorrentItem
		if err := json.Unmarshal(trimmed, &single); err != nil {
			return nil, fmt.Errorf("failed to unmarshal single torrent: %w", err)
		}
		list = []TorrentItem{single}
	}

	return list, nil
}

// GetTorrent fetches a specific torrent by its ID.
func (c *Client) GetTorrent(ctx context.Context, id int) (*TorrentItem, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("TORBOX_API_KEY is not configured")
	}

	endpoint := fmt.Sprintf("%s/torrents/mylist?id=%d", c.BaseURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TorBox mylist returned HTTP %d", resp.StatusCode)
	}

	var res TorboxListResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("failed to decode TorBox response: %w", err)
	}

	trimmed := bytes.TrimSpace(res.Data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, fmt.Errorf("torrent %d not found", id)
	}

	if bytes.HasPrefix(trimmed, []byte("{")) {
		var single TorrentItem
		if err := json.Unmarshal(trimmed, &single); err != nil {
			return nil, err
		}
		return &single, nil
	} else if bytes.HasPrefix(trimmed, []byte("[")) {
		var list []TorrentItem
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return nil, err
		}
		if len(list) > 0 {
			return &list[0], nil
		}
	}
	return nil, fmt.Errorf("torrent %d not found", id)
}

// GetWebDownloads returns the list of direct web downloads in the user's TorBox account.
func (c *Client) GetWebDownloads(ctx context.Context, bypassCache bool) ([]WebDLItem, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("TORBOX_API_KEY is not configured")
	}

	endpoint := fmt.Sprintf("%s/webdl/mylist", c.BaseURL)
	if bypassCache {
		endpoint += "?bypass_cache=true"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TorBox webdl mylist returned HTTP %d", resp.StatusCode)
	}

	var res TorboxListResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("failed to decode TorBox response: %w", err)
	}

	if !res.Success {
		msg := res.Detail
		if msg == "" {
			msg = res.Error
		}
		return nil, fmt.Errorf("TorBox get web downloads failed: %s", msg)
	}

	trimmed := bytes.TrimSpace(res.Data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return []WebDLItem{}, nil
	}

	var list []WebDLItem
	if bytes.HasPrefix(trimmed, []byte("[")) {
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return nil, fmt.Errorf("failed to unmarshal web downloads array: %w", err)
		}
	} else if bytes.HasPrefix(trimmed, []byte("{")) {
		var single WebDLItem
		if err := json.Unmarshal(trimmed, &single); err != nil {
			return nil, fmt.Errorf("failed to unmarshal single web download: %w", err)
		}
		list = []WebDLItem{single}
	}

	return list, nil
}

// GetUploads returns an aggregated and sorted list of all torrents and web downloads.
func (c *Client) GetUploads(ctx context.Context, bypassCache bool) ([]UploadItem, error) {
	var uploads []UploadItem

	torrents, err := c.GetTorrents(ctx, bypassCache)
	if err == nil {
		for _, t := range torrents {
			fileID := BestVideoFileID(t.Files)
			streamURL := c.GetStreamURL("torrent", t.ID, fileID)
			uploads = append(uploads, UploadItem{
				ID:            t.ID,
				Type:          "torrent",
				Name:          t.Name,
				Size:          t.Size,
				SizeFormatted: FormatBytes(t.Size),
				Status:        t.DownloadState,
				Progress:      t.Progress,
				Hash:          t.Hash,
				Files:         t.Files,
				CreatedAt:     t.CreatedAt,
				UpdatedAt:     t.UpdatedAt,
				StreamURL:     streamURL,
			})
		}
	}

	webdls, err := c.GetWebDownloads(ctx, bypassCache)
	if err == nil {
		for _, w := range webdls {
			fileID := BestVideoFileID(w.Files)
			streamURL := c.GetStreamURL("webdl", w.ID, fileID)
			uploads = append(uploads, UploadItem{
				ID:            w.ID,
				Type:          "webdl",
				Name:          w.Name,
				Size:          w.Size,
				SizeFormatted: FormatBytes(w.Size),
				Status:        w.DownloadState,
				Progress:      w.Progress,
				Files:         w.Files,
				CreatedAt:     w.CreatedAt,
				UpdatedAt:     w.UpdatedAt,
				StreamURL:     streamURL,
			})
		}
	}

	// Sort most recent first
	sort.SliceStable(uploads, func(i, j int) bool {
		if uploads[i].CreatedAt != "" && uploads[j].CreatedAt != "" {
			return uploads[i].CreatedAt > uploads[j].CreatedAt
		}
		return false
	})

	return uploads, nil
}

// GetStreamURL constructs the direct TorBox redirect permalink for a file in a torrent or web download.
func (c *Client) GetStreamURL(itemType string, itemID, fileID int) string {
	if c.APIKey == "" {
		return ""
	}
	switch itemType {
	case "webdl":
		if fileID > 0 {
			return fmt.Sprintf("%s/webdl/requestdl?token=%s&web_id=%d&file_id=%d&redirect=true", c.BaseURL, url.QueryEscape(c.APIKey), itemID, fileID)
		}
		return fmt.Sprintf("%s/webdl/requestdl?token=%s&web_id=%d&redirect=true", c.BaseURL, url.QueryEscape(c.APIKey), itemID)
	default: // torrent
		if fileID >= 0 {
			return fmt.Sprintf("%s/torrents/requestdl?token=%s&torrent_id=%d&file_id=%d&redirect=true", c.BaseURL, url.QueryEscape(c.APIKey), itemID, fileID)
		}
		return fmt.Sprintf("%s/torrents/requestdl?token=%s&torrent_id=%d&redirect=true", c.BaseURL, url.QueryEscape(c.APIKey), itemID)
	}
}

// BestVideoFileID chooses the largest video file among torrent/webdl files, or 0 if none.
func BestVideoFileID(files []TorrentFile) int {
	if len(files) == 0 {
		return 0
	}
	videoExts := map[string]bool{
		".mkv": true, ".mp4": true, ".avi": true, ".mov": true,
		".wmv": true, ".webm": true, ".m4v": true, ".ts": true,
	}
	bestID := files[0].ID
	var maxSize int64 = -1
	foundVideo := false

	for _, f := range files {
		ext := strings.ToLower(f.Name)
		if idx := strings.LastIndex(ext, "."); idx != -1 {
			ext = ext[idx:]
		} else {
			ext = ""
		}

		isVideo := videoExts[ext]
		if isVideo {
			if !foundVideo || f.Size > maxSize {
				foundVideo = true
				maxSize = f.Size
				bestID = f.ID
			}
		} else if !foundVideo {
			if f.Size > maxSize {
				maxSize = f.Size
				bestID = f.ID
			}
		}
	}
	return bestID
}

// FormatBytes formats byte count into human-readable unit string.
func FormatBytes(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
		tb = 1024 * gb
	)
	switch {
	case b >= tb:
		return fmt.Sprintf("%.2f TB", float64(b)/float64(tb))
	case b >= gb:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

var (
	UploadYearRegex    = regexp.MustCompile(`\b(19\d\d|20\d\d)\b`)
	UploadTagsRegex    = regexp.MustCompile(`(?i)\b(s\d+(?:e\d+)?|2160p|1080p|720p|480p|4k|uhd|hdr|hdr10|dv|bluray|blu-ray|bdrip|brrip|web-dl|webrip|web|hdtv|dvdrip|remux|x264|x265|hevc|h264|h265|avc|aac|ac3|dts|atmos|ddp5\.1|dd5\.1|sub|dub|multi|tamil|telugu|hindi|malayalam|kannada|english|proper|repack)\b.*$`)
	SeasonEpisodeRegex = regexp.MustCompile(`(?i)\b(?:s|season\s*)(\d+)\s*(?:e|ep|episode\s*)(\d+)\b`)
	UploadDelimRegex   = regexp.MustCompile(`[._]`)
	UploadCleanRegex   = regexp.MustCompile(`[\[\]\(\)\{\}]`)
	UploadSpacesRegex  = regexp.MustCompile(`\s+`)
)

// CleanUploadTitle strips release groups, codecs, quality tags, and extracts year.
func CleanUploadTitle(raw string) (string, int) {
	if idx := strings.LastIndex(raw, "."); idx != -1 && len(raw)-idx <= 5 {
		raw = raw[:idx]
	}
	var year int
	if match := UploadYearRegex.FindString(raw); match != "" {
		if y, err := strconv.Atoi(match); err == nil && y >= 1900 && y <= time.Now().Year()+2 {
			year = y
		}
	}
	s := UploadDelimRegex.ReplaceAllString(raw, " ")
	if year > 0 {
		if idx := strings.Index(s, strconv.Itoa(year)); idx != -1 {
			s = s[:idx]
		}
	} else {
		if loc := UploadTagsRegex.FindStringIndex(s); loc != nil {
			s = s[:loc[0]]
		}
	}
	s = UploadCleanRegex.ReplaceAllString(s, " ")
	s = strings.TrimSpace(UploadSpacesRegex.ReplaceAllString(s, " "))
	return s, year
}

// ParseSeasonEpisode parses S01E02 or Season 1 Episode 2 patterns from filename.
func ParseSeasonEpisode(raw string) (int, int, bool) {
	match := SeasonEpisodeRegex.FindStringSubmatch(raw)
	if len(match) == 3 {
		s, errS := strconv.Atoi(match[1])
		e, errE := strconv.Atoi(match[2])
		if errS == nil && errE == nil {
			return s, e, true
		}
	}
	return 0, 0, false
}
