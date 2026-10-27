package device

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/amxv/stremio-catalog-mcp/pkg/httputil"
)

type DeviceConfig struct {
	Name       string `json:"name"`                  // e.g. "Living Room TV"
	Alias      string `json:"alias"`                 // e.g. "living_room"
	IP         string `json:"ip,omitempty"`          // e.g. "192.168.1.50"
	WebhookURL string `json:"webhook_url,omitempty"` // e.g. "https://webhook.example.com/api/webhook/living_room"
	Platform   string `json:"platform,omitempty"`    // "android_tv", "fire_tv", "home_assistant"
}

type Registry struct {
	RedisURL   string
	RedisToken string
	Client     *http.Client
	mu         sync.Mutex
	mem        []DeviceConfig
}

func NewRegistry(redisURL, redisToken string) *Registry {
	return &Registry{
		RedisURL:   redisURL,
		RedisToken: redisToken,
		Client:     &http.Client{Timeout: 6 * time.Second},
	}
}

func NewRegistryFromEnv() *Registry {
	return NewRegistry(os.Getenv("UPSTASH_REDIS_REST_URL"), os.Getenv("UPSTASH_REDIS_REST_TOKEN"))
}

const devicesRedisKey = "stremio-catalog-mcp:v1:devices"

func (r *Registry) List(ctx context.Context) ([]DeviceConfig, error) {
	if r.RedisURL != "" && r.RedisToken != "" {
		devices, err := r.loadFromRedis(ctx)
		if err == nil && len(devices) > 0 {
			return devices, nil
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.mem) > 0 {
		return r.mem, nil
	}

	// Check if TV_DEVICES_JSON is provided in environment
	if envJSON := os.Getenv("TV_DEVICES_JSON"); envJSON != "" {
		var list []DeviceConfig
		if err := json.Unmarshal([]byte(envJSON), &list); err == nil && len(list) > 0 {
			return list, nil
		}
	}

	// Return empty list if nothing is configured yet
	return []DeviceConfig{}, nil
}

func (r *Registry) Register(ctx context.Context, dev DeviceConfig) error {
	dev.Alias = normalizeAlias(dev.Alias)
	if dev.Alias == "" {
		dev.Alias = normalizeAlias(dev.Name)
	}
	if dev.Alias == "" {
		return fmt.Errorf("device must have a valid name or alias")
	}
	if dev.Platform == "" {
		dev.Platform = "android_tv"
	}

	// Validate device IP if provided (prevent ADB command injection)
	if dev.IP != "" {
		dev.IP = strings.TrimSpace(dev.IP)
		if ip := net.ParseIP(dev.IP); ip == nil {
			return fmt.Errorf("invalid device IP address %q", dev.IP)
		}
		if strings.ContainsAny(dev.IP, ";|&`$<>\"'\n\r ") {
			return fmt.Errorf("device IP contains invalid characters")
		}
	}

	// Validate WebhookURL if provided (SSRF guard against registering malicious internal IPs)
	if dev.WebhookURL != "" {
		dev.WebhookURL = strings.TrimSpace(dev.WebhookURL)
		if err := httputil.ValidatePublicWebhookURL(dev.WebhookURL); err != nil {
			return fmt.Errorf("invalid webhook URL (SSRF protected): %w", err)
		}
	}

	if r.RedisURL != "" && r.RedisToken != "" {
		for attempt := 0; attempt < 5; attempt++ {
			rawPrev, current, err := r.loadRawFromRedis(ctx)
			if err != nil {
				return err
			}
			updated := make([]DeviceConfig, 0, len(current)+1)
			found := false
			for _, d := range current {
				if d.Alias == dev.Alias {
					updated = append(updated, dev)
					found = true
				} else {
					updated = append(updated, d)
				}
			}
			if !found {
				updated = append(updated, dev)
			}
			data, err := json.Marshal(updated)
			if err != nil {
				return err
			}
			ok, err := r.casRedis(ctx, rawPrev, string(data))
			if err != nil {
				return err
			}
			if ok {
				r.mu.Lock()
				r.mem = updated
				r.mu.Unlock()
				return nil
			}
			time.Sleep(10 * time.Millisecond)
		}
		return fmt.Errorf("failed to register device due to concurrent updates (CAS conflict)")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.mem
	updated := make([]DeviceConfig, 0, len(current)+1)
	found := false
	for _, d := range current {
		if d.Alias == dev.Alias {
			updated = append(updated, dev)
			found = true
		} else {
			updated = append(updated, d)
		}
	}
	if !found {
		updated = append(updated, dev)
	}
	r.mem = updated
	return nil
}

func (r *Registry) Remove(ctx context.Context, alias string) error {
	alias = normalizeAlias(alias)

	if r.RedisURL != "" && r.RedisToken != "" {
		for attempt := 0; attempt < 5; attempt++ {
			rawPrev, current, err := r.loadRawFromRedis(ctx)
			if err != nil {
				return err
			}
			updated := make([]DeviceConfig, 0, len(current))
			removed := false
			for _, d := range current {
				if d.Alias == alias {
					removed = true
					continue
				}
				updated = append(updated, d)
			}
			if !removed {
				return fmt.Errorf("device alias %q not found", alias)
			}
			data, err := json.Marshal(updated)
			if err != nil {
				return err
			}
			ok, err := r.casRedis(ctx, rawPrev, string(data))
			if err != nil {
				return err
			}
			if ok {
				r.mu.Lock()
				r.mem = updated
				r.mu.Unlock()
				return nil
			}
			time.Sleep(10 * time.Millisecond)
		}
		return fmt.Errorf("failed to remove device due to concurrent updates (CAS conflict)")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.mem
	updated := make([]DeviceConfig, 0, len(current))
	removed := false
	for _, d := range current {
		if d.Alias == alias {
			removed = true
			continue
		}
		updated = append(updated, d)
	}
	if !removed {
		return fmt.Errorf("device alias %q not found", alias)
	}
	r.mem = updated
	return nil
}

func (r *Registry) Find(ctx context.Context, query string) (*DeviceConfig, error) {
	query = normalizeAlias(query)
	list, err := r.List(ctx)
	if err != nil {
		return nil, err
	}

	// 1. Exact alias match
	for _, d := range list {
		if d.Alias == query {
			match := d
			return &match, nil
		}
	}

	// 2. Contains match (e.g. "living" matches "living_room", "bed" matches "bedroom")
	for _, d := range list {
		if strings.Contains(d.Alias, query) || strings.Contains(query, d.Alias) {
			match := d
			return &match, nil
		}
		nameNorm := normalizeAlias(d.Name)
		if strings.Contains(nameNorm, query) || strings.Contains(query, nameNorm) {
			match := d
			return &match, nil
		}
	}

	if len(list) > 0 {
		def := list[0]
		return &def, nil // Default to first TV if specified query not strictly matched
	}

	return nil, fmt.Errorf("no TV devices configured")
}

func normalizeAlias(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "-", "_")
	var clean strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			clean.WriteRune(r)
		}
	}
	return clean.String()
}

func (r *Registry) loadFromRedis(ctx context.Context) ([]DeviceConfig, error) {
	_, devices, err := r.loadRawFromRedis(ctx)
	return devices, err
}

func (r *Registry) loadRawFromRedis(ctx context.Context) (string, []DeviceConfig, error) {
	endpoint := strings.TrimRight(r.RedisURL, "/")
	reqBody, _ := json.Marshal([]any{"GET", devicesRedisKey})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.RedisToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.Client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()

	var res struct {
		Result *string `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil || res.Result == nil {
		return "", []DeviceConfig{}, nil
	}

	var devices []DeviceConfig
	if err := json.Unmarshal([]byte(*res.Result), &devices); err != nil {
		return *res.Result, []DeviceConfig{}, nil
	}
	return *res.Result, devices, nil
}

func (r *Registry) casRedis(ctx context.Context, previous, next string) (bool, error) {
	const script = `local old = redis.call('GET', KEYS[1]); if (old or '') ~= ARGV[1] then return 0 end; redis.call('SET', KEYS[1], ARGV[2]); return 1`
	endpoint := strings.TrimRight(r.RedisURL, "/")
	reqBody, _ := json.Marshal([]any{"EVAL", script, 1, devicesRedisKey, previous, next})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+r.RedisToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.Client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var res struct {
		Result any `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return false, err
	}
	switch v := res.Result.(type) {
	case float64:
		return v == 1, nil
	case int:
		return v == 1, nil
	case string:
		return v == "1", nil
	}
	return false, nil
}
