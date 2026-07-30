package download

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func rtorrentServer(t *testing.T, response func(string) string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call struct {
			Method string `xml:"methodName"`
		}
		if err := xml.Unmarshal(body, &call); err != nil {
			t.Errorf("request XML: %v", err)
		}
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, response(call.Method))
	}))
}

func TestRTorrentVersionAndListDownloads(t *testing.T) {
	srv := rtorrentServer(t, func(method string) string {
		if method == "system.client_version" {
			return rpcStringResponse("0.9.8")
		}
		return `<methodResponse><params><param><value><array><data><value><array><data>` +
			`<value><string>ABC123</string></value><value><string>Example</string></value>` +
			`<value><string>/downloads</string></value><value><string>/downloads/Example</string></value>` +
			`<value><int>100</int></value><value><int>100</int></value><value><int>0</int></value>` +
			`<value><string></string></value><value><string>librarr</string></value>` +
			`</data></array></value></data></array></value></param></params></methodResponse>`
	})
	defer srv.Close()
	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, Timeout: time.Second, TLSVerify: true})
	info, err := client.TestConnection(t.Context())
	if err != nil || info.Version != "0.9.8" {
		t.Fatalf("version = %+v, err=%v", info, err)
	}
	items, err := client.ListDownloads(t.Context())
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %+v, err=%v", items, err)
	}
	if items[0].InfoHash != "abc123" || !items[0].Completed || items[0].ContentPath != "/downloads/Example" {
		t.Fatalf("item = %+v", items[0])
	}
}

func TestRTorrentErrorsAreSanitized(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"html", "<html>login</html>", "HTML instead of XML-RPC"},
		{"invalid xml", "not xml", "invalid XML-RPC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tc.body) }))
			defer srv.Close()
			client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, Password: "do-not-leak", Timeout: time.Second})
			_, err := client.TestConnection(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "do-not-leak") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestRTorrentAuthenticationAndTimeout(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer authServer.Close()
	client := NewRTorrentClient(RTorrentConfig{URL: authServer.URL, Timeout: time.Second})
	if _, err := client.TestConnection(t.Context()); err == nil || !strings.Contains(err.Error(), "authentication rejected") {
		t.Fatalf("authentication error = %v", err)
	}

	slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		io.WriteString(w, rpcStringResponse("late"))
	}))
	defer slowServer.Close()
	client = NewRTorrentClient(RTorrentConfig{URL: slowServer.URL, Timeout: 20 * time.Millisecond})
	if _, err := client.TestConnection(t.Context()); err == nil {
		t.Fatal("expected timeout")
	}
}

func TestRTorrentNotConfigured(t *testing.T) {
	client := NewRTorrentClient(RTorrentConfig{})
	if _, err := client.TestConnection(t.Context()); err == nil {
		t.Fatal("expected missing URL error")
	}
}

func rpcStringResponse(value string) string {
	return `<methodResponse><params><param><value><string>` + value + `</string></value></param></params></methodResponse>`
}
