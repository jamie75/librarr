package download

import (
	"crypto/md5" // #nosec G501 -- test fixture for HTTP Digest.
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRTorrentDigestAuthentication(t *testing.T) {
	const username, password = "operator", "secret"
	const realm, nonce = "ruTorrent", "fixed-nonce"
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Digest ") {
			w.Header().Set("WWW-Authenticate", `Digest realm="`+realm+`", nonce="`+nonce+`", algorithm=MD5, qop="auth", opaque="opaque-value"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.Contains(auth, `qop=auth`) || !strings.Contains(auth, `nc=00000001`) || !strings.Contains(auth, `opaque="opaque-value"`) {
			t.Fatalf("Digest authorization = %s", auth)
		}
		fields := parseTestDigestAuthorization(auth)
		ha1 := testMD5(username + ":" + realm + ":" + password)
		ha2 := testMD5(r.Method + ":" + fields["uri"])
		want := testMD5(ha1 + ":" + nonce + ":" + fields["nc"] + ":" + fields["cnonce"] + ":auth:" + ha2)
		if fields["response"] != want {
			t.Fatalf("Digest response = %q, want %q", fields["response"], want)
		}
		io.WriteString(w, rpcStringResponse("0.9.7"))
	}))
	defer srv.Close()
	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, Username: username, Password: password, AuthMode: "auto", Timeout: time.Second})
	info, err := client.TestConnection(t.Context())
	if err != nil || info.Version != "0.9.7" || !info.DigestAccepted || info.AuthScheme != "Digest" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	if requests != 2 {
		t.Fatalf("requests=%d, want challenge plus retry", requests)
	}
}

func TestRTorrentDigestWrongPassword(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Digest realm="test", nonce="nonce", qop="auth"`)
		} else {
			w.Header().Set("WWW-Authenticate", `Digest realm="test", nonce="nonce", qop="auth"`)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, Username: "user", Password: "wrong", AuthMode: "digest", Timeout: time.Second})
	if _, err := client.TestConnection(t.Context()); err == nil || !strings.Contains(err.Error(), "authentication rejected") {
		t.Fatalf("error=%v", err)
	}
}

func TestRTorrentDigestRejectsMalformedAndUnsupportedChallenges(t *testing.T) {
	for name, challenge := range map[string]string{
		"malformed":   `Digest realm="test", nonce=`,
		"unsupported": `Digest realm="test", nonce="nonce", algorithm=SHA-256, qop="auth"`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("WWW-Authenticate", challenge)
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer srv.Close()
			client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, Username: "user", Password: "secret", Timeout: time.Second})
			if _, err := client.TestConnection(t.Context()); err == nil {
				t.Fatal("expected Digest challenge error")
			}
		})
	}
}

func TestRTorrentDigestRetriesStaleNonce(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests < 3 {
			nonce := "first"
			stale := "false"
			if requests == 2 {
				nonce, stale = "second", "true"
			}
			w.Header().Set("WWW-Authenticate", `Digest realm="test", nonce="`+nonce+`", qop="auth", stale=`+stale)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, rpcStringResponse("0.9.7"))
	}))
	defer srv.Close()
	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, Username: "user", Password: "secret", AuthMode: "digest", Timeout: time.Second})
	info, err := client.TestConnection(t.Context())
	if err != nil || !info.DigestAccepted || requests != 3 {
		t.Fatalf("info=%+v requests=%d err=%v", info, requests, err)
	}
}

func TestRTorrentRedirectDoesNotForwardDigestCredentials(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("credentials forwarded to another origin")
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer source.Close()
	client := NewRTorrentClient(RTorrentConfig{URL: source.URL, Username: "user", Password: "secret", Timeout: time.Second})
	if _, err := client.TestConnection(t.Context()); err == nil || !strings.Contains(err.Error(), "redirect rejected") {
		t.Fatalf("error=%v", err)
	}
}

func TestRTorrentEndpointFieldsMigrateLegacyURL(t *testing.T) {
	host, port, tlsEnabled, path, err := RTorrentEndpointFields(RTorrentConfig{URL: "https://nl2010.dediseedbox.com:443/rutorrent/plugins/httprpc/action.php"})
	if err != nil || host != "nl2010.dediseedbox.com" || port != 443 || !tlsEnabled || path != "/rutorrent/plugins/httprpc/action.php" {
		t.Fatalf("fields=%q %d %t %q err=%v", host, port, tlsEnabled, path, err)
	}
	endpoint, err := RTorrentEndpoint(RTorrentConfig{Host: "seedbox.example", Port: 443, UseTLS: true, URLPath: "/rpc"})
	if err != nil || endpoint != "https://seedbox.example:443/rpc" {
		t.Fatalf("endpoint=%q err=%v", endpoint, err)
	}
}

func TestRTorrentXMLRPCFaultPreservesCodeAndMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call struct {
			Method string `xml:"methodName"`
		}
		if err := xml.Unmarshal(body, &call); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, rpcFaultResponse(17, "Could not create directory"))
	}))
	defer srv.Close()
	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, Timeout: time.Second})
	_, err := client.SubmitTorrent(TorrentSubmissionRequest{TorrentBytes: validTorrentBytes(), Title: "Book"})
	var fault *RPCFaultError
	if !errors.As(err, &fault) {
		t.Fatalf("error=%v, want RPCFaultError", err)
	}
	if fault.Code != "17" || fault.FaultString != "Could not create directory" || fault.Method != "load.raw_start_verbose" || fault.HTTPStatus != http.StatusOK {
		t.Fatalf("fault=%+v", fault)
	}
	if !strings.Contains(err.Error(), "code 17: Could not create directory") {
		t.Fatalf("error=%v", err)
	}
}

func parseTestDigestAuthorization(header string) map[string]string {
	result := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(header, "Digest "), ",") {
		keyValue := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(keyValue) == 2 {
			result[keyValue[0]] = strings.Trim(keyValue[1], `"`)
		}
	}
	return result
}

func testMD5(value string) string {
	sum := md5.Sum([]byte(value)) // #nosec G401 -- test fixture for HTTP Digest.
	return hex.EncodeToString(sum[:])
}

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

func TestRTorrentSubmitMagnetReturnsStableIdentity(t *testing.T) {
	hash := "abcdef0123456789abcdef0123456789abcdef01"
	srv := rtorrentServer(t, func(method string) string {
		if method == "load.start" {
			return rpcStringResponse(hash)
		}
		return rpcStringResponse("")
	})
	defer srv.Close()
	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, Timeout: time.Second})
	result, err := client.SubmitTorrent(TorrentSubmissionRequest{
		URL: "magnet:?xt=urn:btih:" + hash, Title: "Book", SavePath: "/downloads", Category: "librarr",
	})
	if err != nil {
		t.Fatalf("SubmitTorrent error: %v", err)
	}
	if result.ClientID != "rtorrent" || result.InfoHash != hash || result.RemoteSavePath != "/downloads" {
		t.Fatalf("submission = %+v", result)
	}
}

func TestRTorrentSubmitRawTorrentUsesRawXMLRPC(t *testing.T) {
	seenRaw := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call struct {
			Method string `xml:"methodName"`
		}
		if err := xml.Unmarshal(body, &call); err != nil {
			t.Errorf("request XML: %v", err)
		}
		if call.Method == "load.raw_start" {
			seenRaw = strings.Contains(string(body), "<base64>")
		}
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, rpcStringResponse("abcdef0123456789abcdef0123456789abcdef01"))
	}))
	defer srv.Close()
	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, Timeout: time.Second})
	result, err := client.SubmitTorrent(TorrentSubmissionRequest{TorrentBytes: validTorrentBytes(), Title: "Book"})
	if err != nil {
		t.Fatalf("SubmitTorrent error: %v", err)
	}
	if !seenRaw || result.InfoHash == "" {
		t.Fatalf("raw submission = %+v, seenRaw=%v", result, seenRaw)
	}
}

func TestRTorrentFetchesProwlarrTorrentWithAPIKey(t *testing.T) {
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "prowlarr-secret" {
			t.Fatalf("missing Prowlarr API key")
		}
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write(validTorrentBytes())
	}))
	defer prowlarr.Close()
	rpc := rtorrentServer(t, func(method string) string {
		if method != "load.raw_start" {
			return rpcStringResponse("")
		}
		return rpcStringResponse("abcdef0123456789abcdef0123456789abcdef01")
	})
	defer rpc.Close()
	client := NewRTorrentClient(RTorrentConfig{URL: rpc.URL, ProwlarrURL: prowlarr.URL, ProwlarrAPIKey: "prowlarr-secret", Timeout: time.Second})
	if _, err := client.SubmitTorrent(TorrentSubmissionRequest{URL: prowlarr.URL + "/download.torrent", Title: "Book"}); err != nil {
		t.Fatalf("SubmitTorrent error: %v", err)
	}
}

func rpcStringResponse(value string) string {
	return `<methodResponse><params><param><value><string>` + value + `</string></value></param></params></methodResponse>`
}

func rpcFaultResponse(code int, message string) string {
	return `<methodResponse><fault><value><struct>` +
		`<member><name>faultCode</name><value><int>` + fmt.Sprint(code) + `</int></value></member>` +
		`<member><name>faultString</name><value><string>` + message + `</string></value></member>` +
		`</struct></value></fault></methodResponse>`
}
