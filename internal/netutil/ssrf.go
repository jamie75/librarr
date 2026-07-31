// Package netutil provides network helpers, including SSRF-safe
// validation of user-supplied integration and outbound URLs.
package netutil

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// EndpointPolicy controls outbound integration endpoint validation. Private
// networks are a supported self-hosted deployment target, but metadata and
// infrastructure-only address ranges are never allowed.
type EndpointPolicy struct {
	AllowPrivateNetworks bool
}

// ValidatedEndpoint contains only the origin, path, and query components that
// have passed same-origin validation. It intentionally excludes credentials,
// fragments, and opaque URL data.
type ValidatedEndpoint struct {
	Scheme   string
	Hostname string
	Port     string
	Path     string
	RawQuery string
}

func (e ValidatedEndpoint) URL() *url.URL {
	return &url.URL{
		Scheme:   e.Scheme,
		Host:     net.JoinHostPort(e.Hostname, e.Port),
		Path:     e.Path,
		RawQuery: e.RawQuery,
	}
}

type validatedResolver struct {
	policy EndpointPolicy
	mu     sync.Mutex
	ips    map[string][]net.IP
}

// NewValidatedHTTPClient creates an HTTP client whose dialer connects only to
// addresses validated for endpoint. The URL hostname is retained for TLS
// verification while the dial target is a previously validated IP, avoiding a
// second DNS lookup that could enable rebinding.
func NewValidatedHTTPClient(endpoint string, policy EndpointPolicy, timeout time.Duration, redirect func(*http.Request, []*http.Request) error) (*http.Client, error) {
	u, err := parseStrictHTTPURL(endpoint)
	if err != nil {
		return nil, err
	}
	resolver := &validatedResolver{policy: policy, ips: make(map[string][]net.IP)}
	lookupTimeout := timeout
	if lookupTimeout <= 0 {
		lookupTimeout = 10 * time.Second
	}
	lookupContext, cancel := context.WithTimeout(context.Background(), lookupTimeout)
	defer cancel()
	if _, err := resolver.resolve(lookupContext, u.Hostname()); err != nil {
		return nil, err
	}
	if redirect == nil {
		redirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 8 {
				return fmt.Errorf("too many redirects")
			}
			if _, err := ValidateSameOriginHTTPURL(req.URL.String(), endpoint); err != nil {
				return fmt.Errorf("redirect rejected: %w", err)
			}
			return nil
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = resolver.dialContext
	client := &http.Client{Timeout: lookupTimeout, Transport: transport, CheckRedirect: redirect}
	return client, nil
}

func (r *validatedResolver) resolve(ctx context.Context, host string) ([]net.IP, error) {
	key := strings.ToLower(strings.TrimSuffix(host, "."))
	r.mu.Lock()
	if ips, ok := r.ips[key]; ok {
		r.mu.Unlock()
		return ips, nil
	}
	r.mu.Unlock()

	if ip := net.ParseIP(host); ip != nil {
		if err := validateResolvedAddresses([]net.IP{ip}, r.policy); err != nil {
			return nil, err
		}
		r.mu.Lock()
		r.ips[key] = []net.IP{ip}
		r.mu.Unlock()
		return []net.IP{ip}, nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("endpoint host could not be resolved")
	}
	if err := validateResolvedAddresses(ips, r.policy); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.ips[key] = ips
	r.mu.Unlock()
	return ips, nil
}

func (r *validatedResolver) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid outbound endpoint address")
	}
	ips, err := r.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{}
	var lastErr error
	for _, ip := range ips {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("endpoint has no validated addresses")
}

func validateResolvedAddresses(ips []net.IP, policy EndpointPolicy) error {
	if len(ips) == 0 {
		return fmt.Errorf("endpoint host has no addresses")
	}
	for _, ip := range ips {
		if isInfrastructureBlockedIP(ip) {
			return fmt.Errorf("URL resolves to a restricted address")
		}
		if isRestrictedIP(ip) && !policy.AllowPrivateNetworks {
			return fmt.Errorf("URL resolves to a private address")
		}
	}
	return nil
}

func parseHTTPURL(rawURL string) (*url.URL, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("URL is required")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("URL must use http or https")
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("URL must include a host")
	}
	return u, nil
}

func isMetadataHost(host string) bool {
	lower := strings.ToLower(host)
	for _, blocked := range []string{
		"metadata.google.internal",
		"metadata.goog",
	} {
		if lower == blocked || strings.HasSuffix(lower, "."+blocked) {
			return true
		}
	}
	return false
}

// ValidateIntegrationURL checks admin-initiated integration test URLs (Prowlarr,
// Kavita, etc.). Private and loopback addresses are allowed — homelab services
// commonly run at http://192.168.x.x:port or http://localhost:port.
func ValidateIntegrationURL(rawURL string) error {
	u, err := parseHTTPURL(rawURL)
	if err != nil {
		return err
	}
	if isMetadataHost(u.Hostname()) {
		return fmt.Errorf("URL targets a restricted host")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && isCloudMetadataIP(ip) {
		return fmt.Errorf("URL targets a restricted address")
	}
	return nil
}

// allowPrivateOutbound reports whether the operator has explicitly opted out
// of the private/loopback-address SSRF guard. Some self-hosted setups serve
// downloads from LAN mirrors (a NAS libgen mirror, an internal cache); the
// hermetic e2e suite also relies on it to download from a 127.0.0.1 stub.
// Cloud-metadata addresses stay blocked even with the override — there is no
// legitimate reason for a book download to come from 169.254.169.254.
func allowPrivateOutbound() bool {
	return os.Getenv("LIBRARR_INSECURE_ALLOW_PRIVATE_URLS") == "1"
}

// ValidateOutboundURL checks that rawURL is a safe http(s) target for server-side
// requests. It rejects loopback, private, link-local, and metadata addresses
// (see LIBRARR_INSECURE_ALLOW_PRIVATE_URLS for the LAN-mirror escape hatch).
func ValidateOutboundURL(rawURL string) error {
	u, err := parseHTTPURL(rawURL)
	if err != nil {
		return err
	}
	host := u.Hostname()

	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return fmt.Errorf("URL targets a restricted host")
	}
	if isMetadataHost(host) {
		return fmt.Errorf("URL targets a restricted host")
	}

	if ip := net.ParseIP(host); ip != nil {
		if isCloudMetadataIP(ip) {
			return fmt.Errorf("URL targets a restricted address")
		}
		if isRestrictedIP(ip) && !allowPrivateOutbound() {
			return fmt.Errorf("URL targets a restricted address")
		}
		return nil
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		// Hostname did not resolve (offline, Docker service name, etc.).
		// Blocked hostnames were already rejected above; allow the URL.
		return nil
	}
	for _, ip := range ips {
		if isCloudMetadataIP(ip) {
			return fmt.Errorf("URL resolves to a restricted address")
		}
		if allowPrivateOutbound() {
			continue
		}
		if isRestrictedIP(ip) {
			return fmt.Errorf("URL targets a restricted address")
		}
	}
	return nil
}

// ValidateSameOriginHTTPURL parses rawURL and verifies that it has the same
// normalized host and effective port as allowedOrigin. It is intended for
// integration-owned URLs, such as Prowlarr download links, where the server may
// legitimately be private or loopback but redirects must not escape the configured
// origin.
func ValidateSameOriginHTTPURL(rawURL, allowedOrigin string) (*url.URL, error) {
	u, err := parseStrictHTTPURL(rawURL)
	if err != nil {
		return nil, err
	}
	allowed, err := parseStrictHTTPURL(allowedOrigin)
	if err != nil {
		return nil, fmt.Errorf("configured origin invalid: %w", err)
	}
	if u.Scheme != allowed.Scheme || normalizedHostname(u) != normalizedHostname(allowed) || effectivePort(u) != effectivePort(allowed) {
		return nil, fmt.Errorf("URL host or port does not match configured origin")
	}
	return u, nil
}

// ValidateSameOriginEndpoint validates rawURL against allowedOrigin and
// returns a URL representation rebuilt from approved components only.
func ValidateSameOriginEndpoint(rawURL, allowedOrigin string) (ValidatedEndpoint, error) {
	u, err := parseStrictHTTPURL(rawURL)
	if err != nil {
		return ValidatedEndpoint{}, err
	}
	allowed, err := parseStrictHTTPURL(allowedOrigin)
	if err != nil {
		return ValidatedEndpoint{}, fmt.Errorf("configured origin invalid: %w", err)
	}
	if normalizedHostname(u) != normalizedHostname(allowed) || effectivePort(u) != effectivePort(allowed) || u.Scheme != allowed.Scheme {
		return ValidatedEndpoint{}, fmt.Errorf("URL origin does not match configured origin")
	}
	if u.Fragment != "" {
		return ValidatedEndpoint{}, fmt.Errorf("URL must not include a fragment")
	}
	if u.Opaque != "" {
		return ValidatedEndpoint{}, fmt.Errorf("URL must not include opaque data")
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	decodedPath, err := url.PathUnescape(path)
	if err != nil {
		return ValidatedEndpoint{}, fmt.Errorf("URL path is invalid")
	}
	return ValidatedEndpoint{
		Scheme:   u.Scheme,
		Hostname: normalizedHostname(u),
		Port:     effectivePort(u),
		Path:     decodedPath,
		RawQuery: u.RawQuery,
	}, nil
}

func parseStrictHTTPURL(rawURL string) (*url.URL, error) {
	u, err := parseHTTPURL(rawURL)
	if err != nil {
		return nil, err
	}
	if u.User != nil {
		return nil, fmt.Errorf("URL must not include credentials")
	}
	if u.Fragment != "" {
		return nil, fmt.Errorf("URL must not include a fragment")
	}
	if u.Opaque != "" {
		return nil, fmt.Errorf("URL must not include opaque data")
	}
	if _, err := strictEffectivePort(u); err != nil {
		return nil, err
	}
	return u, nil
}

func normalizedHostname(u *url.URL) string {
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
}

func effectivePort(u *url.URL) string {
	port, _ := strictEffectivePort(u)
	return port
}

func strictEffectivePort(u *url.URL) (string, error) {
	if port := u.Port(); port != "" {
		return port, nil
	}
	host := u.Host
	lastColon := strings.LastIndex(host, ":")
	lastBracket := strings.LastIndex(host, "]")
	if lastColon > lastBracket {
		return "", fmt.Errorf("URL has malformed port")
	}
	switch u.Scheme {
	case "http":
		return "80", nil
	case "https":
		return "443", nil
	default:
		return "", fmt.Errorf("URL must use http or https")
	}
}

// SanitizeLogValue strips control characters and bounds remote-derived values
// before they reach structured logs, so crafted input cannot forge entries or
// create unbounded log records.
func SanitizeLogValue(s string) string {
	const maxLogValue = 2048
	cleaned := make([]rune, 0, min(len(s), maxLogValue))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			cleaned = append(cleaned, ' ')
		} else {
			cleaned = append(cleaned, r)
		}
		if len(cleaned) >= maxLogValue {
			return string(cleaned[:maxLogValue])
		}
	}
	return string(cleaned)
}

// SanitizeSensitiveText redacts credentials and common secret query parameters
// from error strings before they are logged or returned through API responses.
// Line breaks are also removed so the result is safe to log.
func SanitizeSensitiveText(text string) string {
	text = SanitizeLogValue(text)
	parts := strings.Fields(text)
	for i, part := range parts {
		trimmed := strings.Trim(part, "\"'(),")
		if u, err := url.Parse(trimmed); err == nil && u.Scheme != "" && u.Host != "" {
			u.User = nil
			q := u.Query()
			for key := range q {
				lower := strings.ToLower(key)
				if strings.Contains(lower, "key") || strings.Contains(lower, "token") || strings.Contains(lower, "pass") || strings.Contains(lower, "sid") {
					q.Set(key, "REDACTED")
				}
			}
			u.RawQuery = q.Encode()
			parts[i] = strings.Replace(part, trimmed, u.String(), 1)
		}
	}
	return strings.Join(parts, " ")
}

func isCloudMetadataIP(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 != nil && ip4[0] == 169 && ip4[1] == 254 {
		return true
	}
	return ip.Equal(net.ParseIP("fd00:ec2::254"))
}

func isRestrictedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() || ip.IsUnspecified() {
		return true
	}
	return isCloudMetadataIP(ip)
}

func isInfrastructureBlockedIP(ip net.IP) bool {
	return isCloudMetadataIP(ip) || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.Equal(net.IPv4bcast)
}
