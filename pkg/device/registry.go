package device

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type DeviceConfig struct {
	Name       string `json:"name"`                 // e.g. "Living Room TV"
	Alias      string `json:"alias"`                // e.g. "living_room"
	IP         string `json:"ip,omitempty"`          // e.g. "192.168.1.50"
	WebhookURL string `json:"webhook_url,omitempty"`  // e.g. "http://homeassistant:8123/api/webhook/living_room"
	Platform   string `json:"platform,omitempty"`     // "android_tv", "fire_tv", "home_assistant"
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

	// Return standard room placeholders if nothing is configured yet
	return []DeviceConfig{
		{Name: "Living Room TV", Alias: "living_room", Platform: "android_tv"},
		{Name: "Bedroom TV", Alias: "bedroom", Platform: "android_tv"},
		{Name: "Kids Room TV", Alias: "kids_room", Platform: "android_tv"},
	}, nil
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

	current, _ := r.List(ctx)
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

	r.mu.Lock()
	r.mem = updated
	r.mu.Unlock()

	if r.RedisURL != "" && r.RedisToken != "" {
		return r.saveToRedis(ctx, updated)
	}
	return nil
}

func (r *Registry) Remove(ctx context.Context, alias string) error {
	alias = normalizeAlias(alias)
	current, err := r.List(ctx)
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

	r.mu.Lock()
	r.mem = updated
	r.mu.Unlock()

	if r.RedisURL != "" && r.RedisToken != "" {
		return r.saveToRedis(ctx, updated)
	}
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
			return &d, nil
		}
	}

	// 2. Contains match (e.g. "living" matches "living_room", "bed" matches "bedroom")
	for _, d := range list {
		if strings.Contains(d.Alias, query) || strings.Contains(query, d.Alias) {
			return &d, nil
		}
		nameNorm := normalizeAlias(d.Name)
		if strings.Contains(nameNorm, query) || strings.Contains(query, nameNorm) {
			return &d, nil
		}
	}

	if len(list) > 0 {
		return &list[0], nil // Default to first TV if specified query not strictly matched
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
	endpoint := strings.TrimRight(r.RedisURL, "/")
	reqBody, _ := json.Marshal([]any{"GET", devicesRedisKey})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.RedisToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var res struct {
		Result *string `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil || res.Result == nil {
		return nil, fmt.Errorf("no devices in redis")
	}

	var devices []DeviceConfig
	if err := json.Unmarshal([]byte(*res.Result), &devices); err != nil {
		return nil, err
	}
	return devices, nil
}

func (r *Registry) saveToRedis(ctx context.Context, devices []DeviceConfig) error {
	data, err := json.Marshal(devices)
	if err != nil {
		return err
	}

	endpoint := strings.TrimRight(r.RedisURL, "/")
	reqBody, _ := json.Marshal([]any{"SET", devicesRedisKey, string(data)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.RedisToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	io.Copy(io.Discard, resp.Body)
	return nil
}
