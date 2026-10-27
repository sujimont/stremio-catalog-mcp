package device

import (
	"context"
	"net/http"
	"net/http/httptest"
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
