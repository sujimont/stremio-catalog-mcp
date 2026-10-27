package httputil

import (
	"net"
	"testing"
)

func TestIsPrivateOrLocalIP(t *testing.T) {
	tests := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},
		{"127.1.2.3", true},
		{"::1", true},
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.1.1", true},
		{"192.168.0.254", true},
		{"100.64.0.1", true},      // Carrier-Grade NAT (RFC 6598)
		{"100.127.255.254", true}, // Carrier-Grade NAT (RFC 6598)
		{"100.128.0.1", false},    // Outside CGNAT
		{"169.254.169.254", true}, // AWS / cloud metadata IP
		{"169.254.1.1", true},
		{"224.0.0.1", true},       // Multicast
		{"255.255.255.255", true}, // Broadcast
		{"0.0.0.0", true},
		{"8.8.8.8", false},        // Public Google DNS
		{"1.1.1.1", false},        // Public Cloudflare
		{"142.250.190.46", false}, // Public Google
	}

	for _, tt := range tests {
		ip := net.ParseIP(tt.ip)
		if ip == nil {
			t.Fatalf("failed to parse IP %s", tt.ip)
		}
		got := IsPrivateOrLocalIP(ip)
		if got != tt.blocked {
			t.Errorf("IsPrivateOrLocalIP(%s) = %v; want %v", tt.ip, got, tt.blocked)
		}
	}
}

func TestValidatePublicURL(t *testing.T) {
	blockedURLs := []string{
		"http://127.0.0.1/admin",
		"https://127.0.0.1/admin",
		"http://localhost:8080/secret",
		"https://localhost:8080/secret",
		"http://169.254.169.254/latest/meta-data/",
		"https://169.254.169.254/latest/meta-data/",
		"http://10.0.0.5/api",
		"https://10.0.0.5/api",
		"http://192.168.1.50/tv",
		"https://192.168.1.50/tv",
		"http://100.64.0.1/cgnat",
		"https://100.64.0.1/cgnat",
		"file:///etc/passwd",
		"ftp://example.com/test",
		"gopher://example.com",
	}

	for _, u := range blockedURLs {
		if err := ValidatePublicURL(u); err == nil {
			t.Errorf("ValidatePublicURL(%q) expected error, got nil", u)
		}
	}

	// Valid public HTTP and HTTPS IPs (Cloudflare DNS 1.1.1.1)
	if err := ValidatePublicURL("https://1.1.1.1/dns-query"); err != nil {
		t.Errorf("ValidatePublicURL(\"https://1.1.1.1/dns-query\") expected success, got %v", err)
	}
	if err := ValidatePublicURL("http://1.1.1.1/dns-query"); err != nil {
		t.Errorf("ValidatePublicURL(\"http://1.1.1.1/dns-query\") expected success, got %v", err)
	}
}

func TestValidatePublicWebhookURL(t *testing.T) {
	// Plain HTTP must be rejected for webhooks
	if err := ValidatePublicWebhookURL("http://1.1.1.1/webhook"); err == nil {
		t.Errorf("expected error for plain http webhook URL, got nil")
	}

	// Local or private HTTPS must be rejected
	if err := ValidatePublicWebhookURL("https://127.0.0.1/webhook"); err == nil {
		t.Errorf("expected error for local https webhook URL, got nil")
	}

	// Public HTTPS must be allowed
	if err := ValidatePublicWebhookURL("https://1.1.1.1/webhook"); err != nil {
		t.Errorf("expected success for public https webhook URL, got %v", err)
	}
}
