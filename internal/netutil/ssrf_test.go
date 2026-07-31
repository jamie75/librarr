package netutil

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidateIntegrationURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"empty", "", true},
		{"invalid scheme", "ftp://example.com/file", true},
		{"localhost ok", "http://localhost:8080/", false},
		{"127.0.0.1 ok", "http://127.0.0.1/api", false},
		{"private 10.x ok", "http://10.0.0.1/", false},
		{"private 192.168 ok", "http://192.168.70.100:1111/", false},
		{"metadata host blocked", "http://metadata.google.internal/", true},
		{"metadata ip blocked", "http://169.254.169.254/", true},
		{"public https", "https://example.com/path", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateIntegrationURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateIntegrationURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestValidateOutboundURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"empty", "", true},
		{"invalid scheme", "ftp://example.com/file", true},
		{"localhost", "http://localhost:8080/", true},
		{"127.0.0.1", "http://127.0.0.1/api", true},
		{"private 10.x", "http://10.0.0.1/", true},
		{"private 192.168", "http://192.168.1.1/", true},
		{"link-local", "http://169.254.169.254/", true},
		{"metadata host", "http://metadata.google.internal/", true},
		{"public https", "https://example.com/path", false},
		{"public http", "http://prowlarr.example:9696/", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateOutboundURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateOutboundURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestValidateSameOriginHTTPURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		origin  string
		wantErr bool
	}{
		{"same explicit port", "http://prowlarr.example:9696/download", "http://prowlarr.example:9696", false},
		{"same hostname case insensitive", "http://PROWLARR.example:9696/download", "http://prowlarr.EXAMPLE:9696", false},
		{"http default port equals explicit 80", "http://prowlarr.example/download", "http://prowlarr.example:80", false},
		{"https default port equals explicit 443", "https://prowlarr.example/download", "https://prowlarr.example:443", false},
		{"different hostname", "http://other.example:9696/download", "http://prowlarr.example:9696", true},
		{"different port", "http://prowlarr.example:9697/download", "http://prowlarr.example:9696", true},
		{"credentials rejected", "http://user:pass@prowlarr.example:9696/download", "http://prowlarr.example:9696", true},
		{"malformed port rejected", "http://prowlarr.example:bad/download", "http://prowlarr.example:9696", true},
		{"unsupported scheme rejected", "ftp://prowlarr.example/download", "http://prowlarr.example", true},
		{"missing host rejected", "http:///download", "http://prowlarr.example", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidateSameOriginHTTPURL(tt.raw, tt.origin)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateSameOriginHTTPURL() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateSameOriginEndpointRebuildsApprovedComponents(t *testing.T) {
	endpoint, err := ValidateSameOriginEndpoint("HTTPS://PROWLARR.example:443/download/%2Ffile?release=abc%2F123&indexer=7", "https://prowlarr.example")
	if err != nil {
		t.Fatalf("ValidateSameOriginEndpoint() error = %v", err)
	}
	got := endpoint.URL()
	if got.String() != "https://prowlarr.example:443/download//file?release=abc%2F123&indexer=7" {
		t.Fatalf("rebuilt URL = %q", got.String())
	}
	if got.RawQuery != "release=abc%2F123&indexer=7" || got.Fragment != "" || got.User != nil {
		t.Fatalf("rebuilt URL retained unsafe/unapproved components: %#v", got)
	}
}

func TestValidateSameOriginEndpointRejectsFragmentsCredentialsAndForeignOrigins(t *testing.T) {
	tests := []string{
		"https://prowlarr.example/download#fragment",
		"https://user:pass@prowlarr.example/download?release=1",
		"https://other.example/download?release=1",
		"https://prowlarr.example:9443/download?release=1",
		"https:opaque-data",
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if _, err := ValidateSameOriginEndpoint(raw, "https://prowlarr.example"); err == nil {
				t.Fatalf("expected %q to be rejected", raw)
			}
		})
	}
}

func TestSanitizeSensitiveText(t *testing.T) {
	input := `fetch http://user:pass@example.com/download?apikey=secret&token=abc&id=1 failed`
	got := SanitizeSensitiveText(input)
	if strings.Contains(got, "secret") || strings.Contains(got, "abc") || strings.Contains(got, "user:pass") {
		t.Fatalf("SanitizeSensitiveText leaked secret: %q", got)
	}
	if !strings.Contains(got, "id=1") {
		t.Fatalf("SanitizeSensitiveText removed non-secret query: %q", got)
	}
}

func TestNewValidatedHTTPClientPrivateNetworkPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	if _, err := NewValidatedHTTPClient(server.URL, EndpointPolicy{}, time.Second, nil); err == nil {
		t.Fatal("expected private endpoint to be rejected by default")
	}
	client, err := NewValidatedHTTPClient(server.URL, EndpointPolicy{AllowPrivateNetworks: true}, time.Second, nil)
	if err != nil {
		t.Fatalf("allow private endpoint: %v", err)
	}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("validated request: %v", err)
	}
	resp.Body.Close()
}

func TestValidatedTransportAlwaysRejectsInfrastructureAddresses(t *testing.T) {
	for _, raw := range []string{"169.254.169.254", "fd00:ec2::254", "0.0.0.0", "224.0.0.1"} {
		t.Run(raw, func(t *testing.T) {
			if err := validateResolvedAddresses([]net.IP{net.ParseIP(raw)}, EndpointPolicy{AllowPrivateNetworks: true}); err == nil {
				t.Fatalf("expected infrastructure address %s to be rejected", raw)
			}
		})
	}
}

func TestSanitizeLogValueRemovesControlsAndBoundsLength(t *testing.T) {
	got := SanitizeLogValue("prefix\r\n\t\x00\x1b" + strings.Repeat("x", 4096))
	if len([]rune(got)) != 2048 {
		t.Fatalf("sanitized length = %d, want 2048", len([]rune(got)))
	}
	for _, r := range got {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("control character remained in sanitized value: %q", got)
		}
	}
}
