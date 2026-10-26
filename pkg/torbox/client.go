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
