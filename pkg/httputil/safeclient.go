package httputil

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

var (
	// CIDR blocks to block for SSRF prevention
	blockedCIDRs = []*net.IPNet{
		// IPv4 loopback
		mustParseCIDR("127.0.0.0/8"),
		// IPv4 private
		mustParseCIDR("10.0.0.0/8"),
		mustParseCIDR("172.16.0.0/12"),
		mustParseCIDR("192.168.0.0/16"),
		// IPv4 link-local (cloud metadata services, AWS 169.254.169.254)
		mustParseCIDR("169.254.0.0/16"),
		// IPv4 broadcast / unspecified
		mustParseCIDR("0.0.0.0/8"),
		mustParseCIDR("255.255.255.255/32"),
		// IPv4 multicast
		mustParseCIDR("224.0.0.0/4"),

		// IPv6 loopback
		mustParseCIDR("::1/128"),
		// IPv6 unspecified
		mustParseCIDR("::/128"),
		// IPv6 link-local
		mustParseCIDR("fe80::/10"),
		// IPv6 unique local (ULA)
		mustParseCIDR("fc00::/7"),
		// IPv6 multicast
		mustParseCIDR("ff00::/8"),
	}
)

func mustParseCIDR(s string) *net.IPNet {
	_, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		panic(fmt.Sprintf("invalid CIDR %s: %v", s, err))
	}
	return ipnet
}

// IsPrivateOrLocalIP checks whether an IP address belongs to loopback, private LAN,
// link-local (metadata), or multicast ranges.
func IsPrivateOrLocalIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, block := range blockedCIDRs {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

// ValidatePublicURL checks that a URL has a valid http/https scheme and does not resolve
// to any internal, private, loopback, or cloud-metadata IP addresses.
func ValidatePublicURL(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q (only http and https are allowed)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL missing hostname")
	}

	// If hostname is directly an IP address
	if ip := net.ParseIP(host); ip != nil {
		if IsPrivateOrLocalIP(ip) {
			return fmt.Errorf("access to private, local, or link-local IP %s is prohibited", ip)
		}
		return nil
	}

	// Resolve hostname
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("failed to resolve host %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("no IP addresses resolved for host %q", host)
	}

	for _, addr := range addrs {
		if IsPrivateOrLocalIP(addr.IP) {
			return fmt.Errorf("host %q resolves to blocked IP %s", host, addr.IP)
		}
	}

	return nil
}

// NewSafeClient returns an *http.Client configured with SSRF protection:
// 1. Validates resolved IPs before connecting via custom DialContext.
// 2. Re-validates redirected URLs and restricts redirect count.
func NewSafeClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				host = address
			}
			ip := net.ParseIP(host)
			if ip != nil && IsPrivateOrLocalIP(ip) {
				return fmt.Errorf("blocked connection to private/local IP: %s", ip)
			}
			return nil
		},
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			if err := ValidatePublicURL(req.URL.String()); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			return nil
		},
	}
}
