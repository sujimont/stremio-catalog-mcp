package device

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTriggerPlayValidation(t *testing.T) {
	ctx := context.Background()

	// Invalid IMDb ID
	_, err := TriggerPlay(ctx, nil, PlayOnTVInput{
		IMDbID: "bad_id; rm -rf /",
	})
	if err == nil {
		t.Errorf("expected error for malformed IMDb ID, got nil")
	}

	// Invalid media_type
	_, err = TriggerPlay(ctx, nil, PlayOnTVInput{
		IMDbID:    "tt1375666",
		MediaType: "malicious_type",
	})
	if err == nil {
		t.Errorf("expected error for invalid media_type, got nil")
	}

	// Invalid IP with command injection
	_, err = TriggerPlay(ctx, nil, PlayOnTVInput{
		IMDbID:   "tt1375666",
		DeviceIP: "192.168.1.1; echo pwned",
	})
	if err == nil {
		t.Errorf("expected error for shell injection in device IP, got nil")
	}

	// Model-supplied ad-hoc webhook with SSRF target must fail validation
	_, err = TriggerPlay(ctx, nil, PlayOnTVInput{
		IMDbID:     "tt1375666",
		MediaType:  "movie",
		WebhookURL: "http://127.0.0.1:8123/api/webhook",
	})
	if err == nil {
		t.Errorf("expected SSRF error for model-supplied local webhook URL, got nil")
	}

	// Operator-configured webhook (TV_WEBHOOK_URL) is trusted and must not fail validation
	t.Setenv("TV_WEBHOOK_URL", "http://homeassistant.local:8123/api/webhook/test")

	resOperator, err := TriggerPlay(ctx, nil, PlayOnTVInput{
		IMDbID:    "tt1375666",
		MediaType: "movie",
		Title:     "Inception",
	})
	if err != nil {
		t.Fatalf("operator webhook should not fail SSRF validation: %v", err)
	}
	if resOperator.DeepLink != "stremio://detail/movie/tt1375666" {
		t.Errorf("expected deep link stremio://detail/movie/tt1375666, got %s", resOperator.DeepLink)
	}

	// Valid movie play
	res, err := TriggerPlay(ctx, nil, PlayOnTVInput{
		IMDbID:    "tt1375666",
		MediaType: "movie",
		Title:     "Inception",
		DeviceIP:  "192.168.1.100",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.DeepLink != "stremio://detail/movie/tt1375666" {
		t.Errorf("expected deep link stremio://detail/movie/tt1375666, got %s", res.DeepLink)
	}
}

func TestOperatorWebhookNoRedirect(t *testing.T) {
	ctx := context.Background()
	redirectReached := false

	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectReached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer targetSrv.Close()

	redirectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetSrv.URL, http.StatusFound)
	}))
	defer redirectSrv.Close()

	t.Setenv("TV_WEBHOOK_URL", redirectSrv.URL)

	res, err := TriggerPlay(ctx, nil, PlayOnTVInput{
		IMDbID:    "tt1375666",
		MediaType: "movie",
		Title:     "Inception",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Webhook should not follow redirect (ErrUseLastResponse means status 302, not 2xx)
	if res.WebhookSent {
		t.Errorf("operator webhook followed redirect; expected ErrUseLastResponse")
	}
	if redirectReached {
		t.Errorf("target server was reached via redirect; redirects should be stopped")
	}
}

func TestDeviceRegistryValidation(t *testing.T) {
	ctx := context.Background()
	reg := &Registry{}

	// Injection in IP must fail registration
	err := reg.Register(ctx, DeviceConfig{
		Name:  "Hacked TV",
		Alias: "hacked",
		IP:    "192.168.1.50; cat /etc/passwd",
	})
	if err == nil {
		t.Errorf("expected error for command injection in register IP, got nil")
	}

	// SSRF in webhook URL must fail registration
	err = reg.Register(ctx, DeviceConfig{
		Name:       "Metadata TV",
		Alias:      "meta",
		WebhookURL: "http://169.254.169.254/latest/meta-data",
	})
	if err == nil {
		t.Errorf("expected SSRF error for registering cloud metadata webhook, got nil")
	}

	// Plain HTTP in webhook URL must fail registration (requires HTTPS)
	err = reg.Register(ctx, DeviceConfig{
		Name:       "Plain HTTP TV",
		Alias:      "plain",
		WebhookURL: "http://example.com/webhook",
	})
	if err == nil {
		t.Errorf("expected error for registering plain http webhook, got nil")
	}

	// Valid registration
	err = reg.Register(ctx, DeviceConfig{
		Name:     "Living Room TV",
		Alias:    "living_room",
		IP:       "192.168.1.55",
		Platform: "android_tv",
	})
	if err != nil {
		t.Fatalf("failed to register valid TV: %v", err)
	}

	// Look up registered device
	dev, err := reg.Find(ctx, "living_room")
	if err != nil || dev == nil {
		t.Fatalf("failed to find registered device: %v", err)
	}
	if dev.IP != "192.168.1.55" {
		t.Errorf("expected IP 192.168.1.55, got %s", dev.IP)
	}
}

func TestDeviceRegistryRemovalNoResurrection(t *testing.T) {
	ctx := context.Background()
	t.Setenv("TV_DEVICES_JSON", `[{"name":"Fallback TV","alias":"fallback","ip":"192.168.1.99"}]`)

	reg := &Registry{}

	// Initial registration
	err := reg.Register(ctx, DeviceConfig{
		Name:  "Temporary TV",
		Alias: "temp",
		IP:    "192.168.1.10",
	})
	if err != nil {
		t.Fatalf("failed to register device: %v", err)
	}

	// Remove the device
	err = reg.Remove(ctx, "temp")
	if err != nil {
		t.Fatalf("failed to remove device: %v", err)
	}

	// List must return empty, NOT resurrect from TV_DEVICES_JSON!
	list, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 devices after removal, got %d (resurrection bug)", len(list))
	}
}

func TestDeviceRegistrySeeding(t *testing.T) {
	ctx := context.Background()
	t.Setenv("TV_DEVICES_JSON", `[{"name":"Living Room TV","alias":"living_room","ip":"192.168.1.50"}]`)

	reg := &Registry{}

	// First registration should seed from TV_DEVICES_JSON and keep both
	err := reg.Register(ctx, DeviceConfig{
		Name:  "Bedroom TV",
		Alias: "bedroom",
		IP:    "192.168.1.51",
	})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	list, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 devices (seeded + registered), got %d", len(list))
	}
}

func TestDeviceRegistryReset(t *testing.T) {
	ctx := context.Background()
	reg := &Registry{}

	err := reg.Register(ctx, DeviceConfig{
		Name:  "Living Room TV",
		Alias: "living_room",
		IP:    "192.168.1.50",
	})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	err = reg.Reset(ctx)
	if err != nil {
		t.Fatalf("Reset failed: %v", err)
	}

	list, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 devices after reset, got %d", len(list))
	}
}

func TestDeviceRegistryRemoveSeededDevice(t *testing.T) {
	ctx := context.Background()
	t.Setenv("TV_DEVICES_JSON", `[{"name":"Living Room TV","alias":"living_room","ip":"192.168.1.50"},{"name":"Bedroom TV","alias":"bedroom","ip":"192.168.1.51"}]`)

	reg := &Registry{}

	// Initial list should show both seeded devices
	list, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 seeded devices, got %d", len(list))
	}

	// Remove one of the seeded devices
	err = reg.Remove(ctx, "living_room")
	if err != nil {
		t.Fatalf("Remove living_room failed: %v", err)
	}

	// List should now only have bedroom
	list, err = reg.List(ctx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 1 || list[0].Alias != "bedroom" {
		t.Fatalf("expected only bedroom remaining, got %+v", list)
	}

	// Remove bedroom
	err = reg.Remove(ctx, "bedroom")
	if err != nil {
		t.Fatalf("Remove bedroom failed: %v", err)
	}

	// List should now be empty (0 devices) and not resurrect
	list, err = reg.List(ctx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0 devices remaining, got %d", len(list))
	}

	// Removing again should return not found
	err = reg.Remove(ctx, "bedroom")
	if err == nil {
		t.Errorf("expected error removing non-existent device, got nil")
	}
}

func TestTVDevicesJSONValidation(t *testing.T) {
	ctx := context.Background()
	// Mixed entries: one with command injection in IP, one with SSRF webhook, one with plain HTTP webhook, one valid
	t.Setenv("TV_DEVICES_JSON", `[
		{"name":"Injected TV","alias":"injected","ip":"192.168.1.1; echo pwned"},
		{"name":"Metadata TV","alias":"metadata","webhook_url":"http://169.254.169.254/latest/meta-data"},
		{"name":"Plain HTTP TV","alias":"plain_http","webhook_url":"http://example.com/webhook"},
		{"name":"Safe TV","alias":"safe_tv","ip":"192.168.1.60","webhook_url":"https://example.com/webhook"}
	]`)

	reg := &Registry{}
	list, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly 1 valid device, got %d: %+v", len(list), list)
	}
	if list[0].Alias != "safe_tv" {
		t.Errorf("expected safe_tv to survive validation, got %s", list[0].Alias)
	}
}

func TestDeviceRegistryResetRedisError(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"error":"ERR unknown command 'DEL'"}`))
	}))
	defer srv.Close()

	reg := NewRegistry(srv.URL, "fake_token")
	err := reg.Reset(ctx)
	if err == nil {
		t.Fatalf("expected error from Redis DEL, got nil")
	}
	if !strings.Contains(err.Error(), "ERR unknown command") {
		t.Errorf("expected Redis error message, got: %v", err)
	}
}
