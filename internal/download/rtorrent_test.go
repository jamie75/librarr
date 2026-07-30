package download

import (
	"bytes"
	"crypto/md5" // #nosec G501 -- test fixture for HTTP Digest.
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	if fault.Code != "17" || fault.FaultString != "Could not create directory" || fault.Method != "load.raw_start" || fault.HTTPStatus != http.StatusOK {
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
			`<value><int>100</int></value><value><int>100</int></value><value><int>1</int></value>` +
			`<value><int>0</int></value><value><string></string></value><value><string>librarr</string></value>` +
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

func TestRTorrentListDownloadsUsesMainViewAndClassifiesStates(t *testing.T) {
	var calls []rpcRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call rpcRequest
		if err := xml.Unmarshal(body, &call); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, rpcListResponse(
			rpcRow("active-complete", "Seeding", "/downloads/seed", 100, 100, 1, 1, "started", "librarr"),
			rpcRow("stopped-complete", "Done", "/downloads/done", 100, 100, 1, 0, "stopped", "librarr"),
			rpcRow("active-incomplete", "Downloading", "/downloads/active", 100, 40, 0, 1, "started", "librarr"),
			rpcRow("stopped-incomplete", "Stopped", "/downloads/stopped", 100, 40, 0, 0, "stopped", "librarr"),
		))
	}))
	defer srv.Close()

	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, Timeout: time.Second})
	items, err := client.ListDownloads(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].MethodName != "d.multicall2" {
		t.Fatalf("calls = %+v", calls)
	}
	args := rpcRequestStrings(calls[0])
	wantPrefix := []string{"", "main", "d.hash=", "d.name=", "d.base_path=", "d.directory=", "d.size_bytes=", "d.completed_bytes=", "d.complete=", "d.is_active=", "d.state="}
	if len(args) < len(wantPrefix) {
		t.Fatalf("args = %v", args)
	}
	for i, want := range wantPrefix {
		if args[i] != want {
			t.Fatalf("args[%d] = %q, want %q; all args=%v", i, args[i], want, args)
		}
	}
	wantStates := []struct {
		status    string
		completed bool
		progress  float64
	}{
		{"completed", true, 1},
		{"completed", true, 1},
		{"downloading", false, .4},
		{"stopped", false, .4},
	}
	for i, want := range wantStates {
		if items[i].Status != want.status || items[i].Completed != want.completed || items[i].Progress != want.progress {
			t.Fatalf("item[%d] = %+v, want status=%q completed=%t progress=%v", i, items[i], want.status, want.completed, want.progress)
		}
	}
}

func TestRTorrentMissingMainViewReportsAvailableViews(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call rpcRequest
		if err := xml.Unmarshal(body, &call); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/xml")
		if call.MethodName == "d.multicall2" {
			io.WriteString(w, rpcFaultResponse(-503, "Could not find view."))
			return
		}
		if call.MethodName == "view.list" {
			io.WriteString(w, rpcStringListResponse("main", "custom"))
			return
		}
		t.Fatalf("unexpected method %q", call.MethodName)
	}))
	defer srv.Close()

	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, Timeout: time.Second})
	_, err := client.ListDownloads(t.Context())
	if err == nil || !strings.Contains(err.Error(), "available views: main, custom") {
		t.Fatalf("error = %v", err)
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

func TestRTorrentMagnetUsesEmptyTargetArgument(t *testing.T) {
	hash := "abcdef0123456789abcdef0123456789abcdef01"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call rpcRequest
		if err := xml.Unmarshal(body, &call); err != nil {
			t.Fatal(err)
		}
		if call.MethodName == "load.start" {
			if len(call.Params) != 2 || call.Params[0].Value.String == nil || *call.Params[0].Value.String != "" || call.Params[1].Value.String == nil {
				t.Fatalf("method=%s params=%+v", call.MethodName, call.Params)
			}
			io.WriteString(w, rpcStringResponse(hash))
			return
		}
		io.WriteString(w, rpcStringResponse(""))
	}))
	defer srv.Close()
	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, Timeout: time.Second})
	if _, err := client.SubmitTorrent(TorrentSubmissionRequest{URL: "magnet:?xt=urn:btih:" + hash, Title: "Book"}); err != nil {
		t.Fatalf("SubmitTorrent error: %v", err)
	}
}

func TestRTorrentSubmitRawTorrentUsesRawXMLRPC(t *testing.T) {
	seenRaw := false
	var seenParams []rpcParam
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call rpcRequest
		if err := xml.Unmarshal(body, &call); err != nil {
			t.Errorf("request XML: %v", err)
		}
		if call.MethodName == "load.raw_start" {
			seenRaw = strings.Contains(string(body), "<base64>")
			seenParams = call.Params
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
	if !seenRaw || result.InfoHash == "" || len(seenParams) != 2 || seenParams[0].Value.String == nil || *seenParams[0].Value.String != "" || seenParams[1].Value.Base64 == nil {
		t.Fatalf("raw submission = %+v, seenRaw=%v", result, seenRaw)
	}
	decoded, err := base64.StdEncoding.DecodeString(*seenParams[1].Value.Base64)
	if err != nil || string(decoded) != string(validTorrentBytes()) {
		t.Fatalf("decoded torrent bytes do not match original: err=%v", err)
	}
	if got, want := sha256.Sum256(decoded), sha256.Sum256(validTorrentBytes()); got != want {
		t.Fatalf("decoded torrent SHA-256 = %x, want %x", got, want)
	}
}

func TestRTorrentRawStartTooFewArgumentsRegression(t *testing.T) {
	methods := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call rpcRequest
		if err := xml.Unmarshal(body, &call); err != nil {
			t.Fatal(err)
		}
		if call.MethodName == "load.raw_start" || call.MethodName == "load.raw_start_verbose" {
			methods = append(methods, call.MethodName)
		}
		if call.MethodName == "load.raw_start" && len(call.Params) == 2 {
			io.WriteString(w, rpcStringResponse("abcdef0123456789abcdef0123456789abcdef01"))
			return
		}
		io.WriteString(w, rpcFaultResponse(-503, "Too few arguments."))
	}))
	defer srv.Close()
	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, Timeout: time.Second})
	if _, err := client.SubmitTorrent(TorrentSubmissionRequest{TorrentBytes: validTorrentBytes(), Title: "Book"}); err != nil {
		t.Fatalf("SubmitTorrent error: %v", err)
	}
	if len(methods) != 1 || methods[0] != "load.raw_start" {
		t.Fatalf("methods=%v, want only corrected load.raw_start", methods)
	}
}

func TestRTorrentRawStartVerboseFallbackIsNarrow(t *testing.T) {
	methods := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call rpcRequest
		if err := xml.Unmarshal(body, &call); err != nil {
			t.Fatal(err)
		}
		if call.MethodName == "load.raw_start" || call.MethodName == "load.raw_start_verbose" {
			methods = append(methods, call.MethodName)
		}
		if call.MethodName == "load.raw_start" {
			io.WriteString(w, rpcFaultResponse(-501, "Method not found"))
			return
		}
		io.WriteString(w, rpcStringResponse("abcdef0123456789abcdef0123456789abcdef01"))
	}))
	defer srv.Close()
	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, Timeout: time.Second})
	if _, err := client.SubmitTorrent(TorrentSubmissionRequest{TorrentBytes: validTorrentBytes(), Title: "Book"}); err != nil {
		t.Fatalf("SubmitTorrent error: %v", err)
	}
	if len(methods) != 2 || methods[1] != "load.raw_start_verbose" {
		t.Fatalf("methods=%v, want narrow verbose fallback", methods)
	}
}

func TestRTorrentStoppedRawSubmissionUsesLoadRawAndInitialCommands(t *testing.T) {
	hash, err := torrentInfoHash(validTorrentBytes())
	if err != nil {
		t.Fatal(err)
	}
	var calls []rpcRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call rpcRequest
		if err := xml.Unmarshal(body, &call); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
		if call.MethodName == "load.raw" {
			io.WriteString(w, rpcIntResponse(0))
			return
		}
		io.WriteString(w, rpcListResponse(rpcRow(hash, "Book", "/remote/downloads/Book", 100, 0, 0, 0, "stopped", "librarr")))
	}))
	defer srv.Close()

	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, LabelField: "d.custom1=", Timeout: time.Second})
	if _, err := client.SubmitTorrent(TorrentSubmissionRequest{TorrentBytes: validTorrentBytes(), Title: "Book", SavePath: "/remote/downloads", Category: "librarr", AddStopped: true}); err != nil {
		t.Fatal(err)
	}
	if len(calls) < 1 || calls[0].MethodName != "load.raw" {
		t.Fatalf("calls=%+v, want load.raw first", calls)
	}
	if got := rpcRequestStrings(calls[0]); len(got) != 4 || got[0] != "" || got[2] != "d.custom1.set=librarr" || got[3] != "d.directory.set=/remote/downloads" {
		t.Fatalf("load.raw args=%v", got)
	}
}

func TestRTorrentMagnetSubmissionIncludesInitialCommands(t *testing.T) {
	hash := "abcdef0123456789abcdef0123456789abcdef01"
	var calls []rpcRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call rpcRequest
		if err := xml.Unmarshal(body, &call); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
		if call.MethodName == "load.start" {
			io.WriteString(w, rpcIntResponse(0))
			return
		}
		io.WriteString(w, rpcListResponse(rpcRow(hash, "Book", "/remote/downloads/Book", 100, 0, 0, 1, "started", "librarr")))
	}))
	defer srv.Close()

	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, LabelField: "d.custom1=", Timeout: time.Second})
	if _, err := client.SubmitTorrent(TorrentSubmissionRequest{URL: "magnet:?xt=urn:btih:" + hash, Title: "Book", SavePath: "/remote/downloads", Category: "librarr"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) < 1 || calls[0].MethodName != "load.start" {
		t.Fatalf("calls=%+v, want load.start first", calls)
	}
	if got := rpcRequestStrings(calls[0]); len(got) != 4 || got[0] != "" || got[1] != "magnet:?xt=urn:btih:"+hash || got[2] != "d.custom1.set=librarr" || got[3] != "d.directory.set=/remote/downloads" {
		t.Fatalf("load.start args=%v", got)
	}
}

func TestRTorrentStoppedMagnetUsesLoadNormal(t *testing.T) {
	hash := "abcdef0123456789abcdef0123456789abcdef01"
	method := ""
	srv := rtorrentServer(t, func(name string) string {
		if strings.HasPrefix(name, "load.") {
			method = name
		}
		if name == "load.normal" {
			return rpcIntResponse(0)
		}
		return rpcStringResponse("")
	})
	defer srv.Close()
	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, Timeout: time.Second})
	if _, err := client.SubmitTorrent(TorrentSubmissionRequest{URL: "magnet:?xt=urn:btih:" + hash, Title: "Book", AddStopped: true}); err != nil {
		t.Fatal(err)
	}
	if method != "load.normal" {
		t.Fatalf("method=%q, want load.normal", method)
	}
}

func TestRTorrentNonzeroLoadResponseFailsSubmission(t *testing.T) {
	srv := rtorrentServer(t, func(method string) string {
		if method == "load.raw_start" {
			return rpcIntResponse(1)
		}
		return rpcStringResponse("")
	})
	defer srv.Close()
	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, Timeout: time.Second})
	if _, err := client.SubmitTorrent(TorrentSubmissionRequest{TorrentBytes: validTorrentBytes(), Title: "Book"}); err == nil || !strings.Contains(err.Error(), "response 1") {
		t.Fatalf("error=%v, want nonzero load response failure", err)
	}
}

func TestRTorrentWrongDirectoryReadbackLogsWarning(t *testing.T) {
	hash, err := torrentInfoHash(validTorrentBytes())
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call rpcRequest
		if err := xml.Unmarshal(body, &call); err != nil {
			t.Fatal(err)
		}
		if call.MethodName == "load.raw_start" {
			io.WriteString(w, rpcIntResponse(0))
			return
		}
		io.WriteString(w, rpcListResponse(rpcRow(hash, "Book", "/wrong/path/Book", 100, 0, 0, 1, "started", "librarr")))
	}))
	defer srv.Close()
	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, Timeout: time.Second})
	if _, err := client.SubmitTorrent(TorrentSubmissionRequest{TorrentBytes: validTorrentBytes(), Title: "Book", SavePath: "/remote/downloads", Category: "librarr"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "different directory") || !strings.Contains(logs.String(), "requested_directory=/remote/downloads") {
		t.Fatalf("logs=%s, want wrong-directory warning", logs.String())
	}
}

func TestRTorrentSubmissionAppliesRemotePathAndOptionalLabel(t *testing.T) {
	var calls [][]string
	hash, err := torrentInfoHash(validTorrentBytes())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call rpcRequest
		if err := xml.Unmarshal(body, &call); err != nil {
			t.Fatal(err)
		}
		args := []string{}
		for _, param := range call.Params {
			args = append(args, debugRPCArgs([]rpcValue{param.Value})...)
		}
		calls = append(calls, append([]string{call.MethodName}, args...))
		if call.MethodName == "load.raw_start" {
			io.WriteString(w, rpcIntResponse(0))
			return
		}
		if call.MethodName == "d.multicall2" {
			io.WriteString(w, rpcListResponse(rpcRow(hash, "Book", "/remote/downloads/Book", 100, 0, 0, 1, "started", "librarr")))
			return
		}
		io.WriteString(w, rpcStringResponse(""))
	}))
	defer srv.Close()
	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, LabelField: "d.custom1=librarr", Timeout: time.Second})
	result, err := client.SubmitTorrent(TorrentSubmissionRequest{TorrentBytes: validTorrentBytes(), Title: "Book", SavePath: "/remote/downloads", Category: "librarr"})
	if err != nil {
		t.Fatalf("SubmitTorrent error: %v", err)
	}
	if result.InfoHash != hash {
		t.Fatalf("result hash=%q, want %q", result.InfoHash, hash)
	}
	if !containsRPCArgs(calls, "load.raw_start", "", "<torrent bytes omitted>", "d.custom1.set=librarr", "d.directory.set=/remote/downloads") {
		t.Fatalf("calls=%v, missing initial label/directory commands", calls)
	}
	for _, call := range calls {
		if call[0] == "d.directory.set" || call[0] == "d.custom1.set" {
			t.Fatalf("post-load setter should not be primary path: calls=%v", calls)
		}
	}
}

func TestRTorrentLabelFailureDoesNotBlockSubmission(t *testing.T) {
	hash, err := torrentInfoHash(validTorrentBytes())
	if err != nil {
		t.Fatal(err)
	}
	loadCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call rpcRequest
		if err := xml.Unmarshal(body, &call); err != nil {
			t.Fatal(err)
		}
		if call.MethodName == "load.raw_start" {
			loadCalls++
			if loadCalls == 1 {
				io.WriteString(w, rpcFaultResponse(-503, "label rejected"))
				return
			}
			io.WriteString(w, rpcIntResponse(0))
			return
		}
		if call.MethodName == "d.multicall2" {
			io.WriteString(w, rpcListResponse(rpcRow(hash, "Book", "/remote/downloads/Book", 100, 0, 0, 1, "started", "")))
			return
		}
		io.WriteString(w, rpcFaultResponse(-503, "label rejected"))
	}))
	defer srv.Close()
	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, LabelField: "d.custom1=", Timeout: time.Second})
	if _, err := client.SubmitTorrent(TorrentSubmissionRequest{TorrentBytes: validTorrentBytes(), Title: "Book", Category: "librarr"}); err != nil {
		t.Fatalf("label failure blocked submission: %v", err)
	}
	if loadCalls != 2 {
		t.Fatalf("load calls=%d, want one label attempt and one unlabeled retry", loadCalls)
	}
}

func containsRPCArgs(calls [][]string, method string, args ...string) bool {
	for _, call := range calls {
		if len(call) != len(args)+1 || call[0] != method {
			continue
		}
		matched := true
		for i, arg := range args {
			if call[i+1] != arg {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
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

func rpcRequestStrings(call rpcRequest) []string {
	args := make([]string, 0, len(call.Params))
	for _, param := range call.Params {
		args = append(args, valueString(param.Value))
	}
	return args
}

func rpcRow(hash, name, path string, size, completed, complete, active int64, state, label string) string {
	return `<value><array><data>` +
		`<value><string>` + hash + `</string></value>` +
		`<value><string>` + name + `</string></value>` +
		`<value><string>` + path + `</string></value>` +
		`<value><string>` + path + `</string></value>` +
		`<value><int>` + fmt.Sprint(size) + `</int></value>` +
		`<value><int>` + fmt.Sprint(completed) + `</int></value>` +
		`<value><int>` + fmt.Sprint(complete) + `</int></value>` +
		`<value><int>` + fmt.Sprint(active) + `</int></value>` +
		`<value><string>` + state + `</string></value>` +
		`<value><string>` + label + `</string></value>` +
		`</data></array></value>`
}

func rpcListResponse(rows ...string) string {
	return `<methodResponse><params><param><value><array><data>` + strings.Join(rows, "") +
		`</data></array></value></param></params></methodResponse>`
}

func rpcStringListResponse(values ...string) string {
	rows := make([]string, 0, len(values))
	for _, value := range values {
		rows = append(rows, `<value><string>`+value+`</string></value>`)
	}
	return `<methodResponse><params><param><value><array><data>` + strings.Join(rows, "") +
		`</data></array></value></param></params></methodResponse>`
}

func rpcStringResponse(value string) string {
	return `<methodResponse><params><param><value><string>` + value + `</string></value></param></params></methodResponse>`
}

func rpcIntResponse(value int) string {
	return `<methodResponse><params><param><value><int>` + fmt.Sprint(value) + `</int></value></param></params></methodResponse>`
}

func rpcFaultResponse(code int, message string) string {
	return `<methodResponse><fault><value><struct>` +
		`<member><name>faultCode</name><value><int>` + fmt.Sprint(code) + `</int></value></member>` +
		`<member><name>faultString</name><value><string>` + message + `</string></value></member>` +
		`</struct></value></fault></methodResponse>`
}
