package download

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jamie75/librarr/internal/diagnostics"
	"github.com/jamie75/librarr/internal/netutil"
)

// RTorrentConfig contains rTorrent XML-RPC connection and submission settings.
type RTorrentConfig struct {
	Name           string
	URL            string
	Host           string
	Port           int
	UseTLS         bool
	URLPath        string
	Username       string
	Password       string
	AuthMode       string
	Timeout        time.Duration
	LabelField     string
	TLSVerify      bool
	ProwlarrURL    string
	ProwlarrAPIKey string
}

// ReadOnlyDownloadClient is the client-neutral surface used by inspection and
// future completion tracking. It intentionally has no add/delete methods.
type ReadOnlyDownloadClient interface {
	ClientID() string
	Name() string
	Type() string
	TestConnection(context.Context) (ClientInfo, error)
	ListDownloads(context.Context) ([]ClientDownload, error)
	GetDownload(context.Context, string) (ClientDownload, error)
}

type ClientInfo struct {
	Version        string `json:"version,omitempty"`
	AuthScheme     string `json:"auth_scheme,omitempty"`
	DigestAccepted bool   `json:"digest_accepted,omitempty"`
	HTTPStatus     int    `json:"http_status,omitempty"`
	LatencyMillis  int64  `json:"latency_ms,omitempty"`
}

type ClientDownload struct {
	ID          string  `json:"id"`
	InfoHash    string  `json:"info_hash,omitempty"`
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	Progress    float64 `json:"progress"`
	SavePath    string  `json:"save_path,omitempty"`
	ContentPath string  `json:"content_path,omitempty"`
	Size        int64   `json:"size"`
	Completed   bool    `json:"completed"`
	Label       string  `json:"label,omitempty"`
	Error       string  `json:"error,omitempty"`
}

// RTorrentClient talks to rTorrent's XML-RPC endpoint directly and never
// scrapes or automates ruTorrent. Removal is intentionally unsupported.
type RTorrentClient struct {
	cfg    RTorrentConfig
	client *http.Client
}

var _ ReadOnlyDownloadClient = (*RTorrentClient)(nil)

func NewRTorrentClient(cfg RTorrentConfig) *RTorrentClient {
	if cfg.Name == "" {
		cfg.Name = "rTorrent"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.LabelField == "" {
		cfg.LabelField = "d.custom1="
	}
	if cfg.AuthMode == "" {
		cfg.AuthMode = "auto"
	}
	if normalized, err := normalizeRTorrentConfig(cfg); err == nil {
		cfg = normalized
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: !cfg.TLSVerify} // #nosec G402 -- explicit admin setting
	endpoint := cfg.URL
	return &RTorrentClient{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout, Transport: transport, CheckRedirect: sameRTorrentOriginRedirect(endpoint)}}
}

func (r *RTorrentClient) ClientID() string { return "rtorrent" }
func (r *RTorrentClient) Name() string     { return r.cfg.Name }
func (r *RTorrentClient) Type() string     { return "rtorrent" }

func (r *RTorrentClient) AddTorrent(torrentURL, title, savePath, category, expectedInfoHash string) error {
	_, err := r.SubmitTorrent(TorrentSubmissionRequest{URL: torrentURL, Title: title, SavePath: savePath, Category: category, ExpectedInfoHash: expectedInfoHash})
	return err
}

func (r *RTorrentClient) SubmitTorrent(request TorrentSubmissionRequest) (TorrentSubmission, error) {
	if err := validateRTorrentSubmission(request); err != nil {
		return TorrentSubmission{}, err
	}
	if len(request.TorrentBytes) == 0 && !strings.HasPrefix(strings.ToLower(request.URL), "magnet:") {
		fetched, err := fetchRTorrentTorrent(request.URL, r.cfg)
		if err != nil {
			return TorrentSubmission{}, err
		}
		request.TorrentBytes = fetched
	}
	hash := firstNonEmptyHash(request.ExpectedInfoHash, infoHashFromMagnet(request.URL))
	if len(request.TorrentBytes) > 0 {
		calculated, err := torrentInfoHash(bytes.TrimSpace(request.TorrentBytes))
		if err != nil {
			return TorrentSubmission{}, fmt.Errorf("invalid torrent payload: %w", err)
		}
		hash = calculated
	}
	method := "load.start"
	var value rpcValue
	var err error
	if strings.HasPrefix(strings.ToLower(request.URL), "magnet:") {
		slog.Info("submitting magnet to rTorrent", "client_id", r.ClientID(), "title", netutil.SanitizeLogValue(request.Title), "category", netutil.SanitizeLogValue(request.Category))
	} else {
		slog.Info("uploading torrent bytes to rTorrent", "client_id", r.ClientID(), "title", netutil.SanitizeLogValue(request.Title), "category", netutil.SanitizeLogValue(request.Category), "bytes", len(request.TorrentBytes))
	}
	if len(request.TorrentBytes) > 0 {
		method = "load.raw_start"
		encoded := base64.StdEncoding.EncodeToString(request.TorrentBytes)
		value, err = r.call(context.Background(), method, rpcValue{Base64: stringPtr(encoded)})
		if err != nil {
			// Older rTorrent builds expose the verbose raw loader instead.
			method = "load.raw_start_verbose"
			value, err = r.call(context.Background(), method, rpcValue{Base64: stringPtr(encoded)})
		}
	} else {
		value, err = r.call(context.Background(), method, rpcValue{String: stringPtr(request.URL)})
	}
	if err != nil {
		return TorrentSubmission{}, err
	}
	if hash == "" {
		hash = strings.ToLower(strings.TrimSpace(valueString(value)))
	}
	if hash == "" {
		return TorrentSubmission{}, fmt.Errorf("rTorrent accepted the submission but returned no torrent hash")
	}
	if request.SavePath != "" {
		if _, err := r.call(context.Background(), "d.directory.set", rpcValue{String: stringPtr(hash)}, rpcValue{String: stringPtr(request.SavePath)}); err != nil {
			return TorrentSubmission{}, fmt.Errorf("rTorrent destination setup failed: %w", err)
		}
	}
	if request.Category != "" && strings.HasSuffix(r.cfg.LabelField, "=") {
		field := strings.TrimSuffix(r.cfg.LabelField, "=")
		field = strings.TrimSuffix(field, ".") + ".set"
		if _, err := r.call(context.Background(), field, rpcValue{String: stringPtr(hash)}, rpcValue{String: stringPtr(request.Category)}); err != nil {
			return TorrentSubmission{}, fmt.Errorf("rTorrent label setup failed: %w", err)
		}
	}
	return TorrentSubmission{ClientID: r.ClientID(), ClientType: r.Type(), DownloadID: hash, InfoHash: hash, Name: request.Title, RemoteSavePath: request.SavePath, Category: request.Category}, nil
}

func validateRTorrentSubmission(request TorrentSubmissionRequest) error {
	value := strings.TrimSpace(request.URL)
	if value == "" && len(request.TorrentBytes) == 0 {
		return fmt.Errorf("torrent URL or magnet is required")
	}
	if len(request.TorrentBytes) > 2<<20 {
		return fmt.Errorf("torrent payload exceeds the 2 MiB limit")
	}
	if len(request.TorrentBytes) > 0 {
		if len(bytes.TrimSpace(request.TorrentBytes)) == 0 || bytes.TrimSpace(request.TorrentBytes)[0] != 'd' {
			return fmt.Errorf("torrent payload is not a bencode dictionary")
		}
	} else if strings.HasPrefix(strings.ToLower(value), "magnet:") {
		if infoHashFromMagnet(value) == "" {
			return fmt.Errorf("magnet is missing a valid info hash")
		}
	} else {
		u, err := url.Parse(value)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return fmt.Errorf("torrent URL must be an HTTP or HTTPS URL without credentials")
		}
	}
	if request.SavePath != "" {
		clean := filepath.Clean(strings.TrimSpace(request.SavePath))
		if !filepath.IsAbs(clean) || clean == "." || clean == string(filepath.Separator)+".." || strings.HasPrefix(clean, string(filepath.Separator)+".."+string(filepath.Separator)) {
			return fmt.Errorf("rTorrent save path must be an absolute remote path")
		}
	}
	if len(request.Category) > 100 || strings.ContainsAny(request.Category, "\r\n") {
		return fmt.Errorf("invalid rTorrent label")
	}
	return nil
}

func fetchRTorrentTorrent(rawURL string, cfg RTorrentConfig) ([]byte, error) {
	if strings.TrimSpace(cfg.ProwlarrURL) == "" {
		return nil, fmt.Errorf("rTorrent torrent URL requires configured Prowlarr access")
	}
	if _, err := netutil.ValidateSameOriginHTTPURL(rawURL, cfg.ProwlarrURL); err != nil {
		return nil, fmt.Errorf("rTorrent torrent URL rejected: %w", err)
	}
	client := &http.Client{Timeout: cfg.Timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 8 {
			return fmt.Errorf("too many redirects")
		}
		if _, err := netutil.ValidateSameOriginHTTPURL(req.URL.String(), cfg.ProwlarrURL); err != nil {
			return fmt.Errorf("redirect rejected: %w", err)
		}
		return nil
	}}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build rTorrent torrent request: %w", err)
	}
	if cfg.ProwlarrAPIKey != "" {
		req.Header.Set("X-Api-Key", cfg.ProwlarrAPIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch rTorrent torrent: %s", netutil.SanitizeSensitiveText(err.Error()))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTorrentFetchBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read rTorrent torrent: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch rTorrent torrent HTTP %d", resp.StatusCode)
	}
	if len(body) > maxTorrentFetchBytes {
		return nil, fmt.Errorf("torrent response too large")
	}
	if _, err := validateTorrentBytes(resp.Header.Get("Content-Type"), body); err != nil {
		return nil, err
	}
	return body, nil
}

func (r *RTorrentClient) TestConnection(ctx context.Context) (ClientInfo, error) {
	started := time.Now()
	version, probe, err := r.callDetailed(ctx, "system.client_version")
	if err != nil {
		return ClientInfo{}, err
	}
	return ClientInfo{Version: valueString(version), AuthScheme: probe.authScheme, DigestAccepted: probe.digestAccepted, HTTPStatus: probe.status, LatencyMillis: time.Since(started).Milliseconds()}, nil
}

func (r *RTorrentClient) ListDownloads(ctx context.Context) ([]ClientDownload, error) {
	fields := []string{"d.hash=", "d.name=", "d.base_path=", "d.directory=", "d.size_bytes=", "d.completed_bytes=", "d.is_active=", "d.state=", r.cfg.LabelField}
	params := make([]rpcValue, 0, len(fields)+1)
	params = append(params, rpcValue{String: stringPtr("")})
	for _, field := range fields {
		params = append(params, rpcValue{String: stringPtr(field)})
	}
	value, err := r.call(ctx, "d.multicall2", params...)
	if err != nil {
		return nil, err
	}
	rows, ok := value.Array()
	if !ok {
		return nil, fmt.Errorf("rTorrent returned an invalid download list")
	}
	downloads := make([]ClientDownload, 0, len(rows))
	for _, row := range rows {
		values, ok := row.Array()
		if !ok || len(values) < 8 {
			return nil, fmt.Errorf("rTorrent returned a malformed download row")
		}
		size := valueInt(values[4])
		completedBytes := valueInt(values[5])
		progress := 0.0
		if size > 0 {
			progress = float64(completedBytes) / float64(size)
		}
		if progress > 1 {
			progress = 1
		}
		active := valueInt(values[6]) != 0
		state := valueString(values[7])
		if state == "" {
			state = mapRTorrentState(active, progress)
		}
		basePath := valueString(values[2])
		directory := valueString(values[3])
		contentPath := directory
		if contentPath == "" {
			contentPath = basePath
		}
		downloads = append(downloads, ClientDownload{
			ID:          valueString(values[0]),
			InfoHash:    strings.ToLower(valueString(values[0])),
			Name:        valueString(values[1]),
			Status:      state,
			Progress:    progress,
			SavePath:    basePath,
			ContentPath: contentPath,
			Size:        size,
			Completed:   progress >= 1,
			Label:       valueString(values[len(values)-1]),
		})
	}
	return downloads, nil
}

func (r *RTorrentClient) GetDownload(ctx context.Context, id string) (ClientDownload, error) {
	items, err := r.ListDownloads(ctx)
	if err != nil {
		return ClientDownload{}, err
	}
	for _, item := range items {
		if strings.EqualFold(item.ID, id) || strings.EqualFold(item.InfoHash, id) {
			return item, nil
		}
	}
	return ClientDownload{}, fmt.Errorf("rTorrent download %q was not found", id)
}

func (r *RTorrentClient) GetTorrents(category string) ([]TorrentInfo, error) {
	items, err := r.ListDownloads(context.Background())
	if err != nil {
		return nil, err
	}
	result := make([]TorrentInfo, 0, len(items))
	for _, item := range items {
		if category != "" && item.Label != category {
			continue
		}
		result = append(result, TorrentInfo{Name: item.Name, ContentPath: item.ContentPath, SavePath: item.SavePath, Hash: item.InfoHash, State: item.Status, Progress: item.Progress, TotalSize: item.Size, Category: item.Label})
	}
	return result, nil
}

func (r *RTorrentClient) GetTorrentFiles(string) ([]TorrentFile, error) { return nil, nil }

func (r *RTorrentClient) DeleteTorrent(string, bool) error {
	return fmt.Errorf("rTorrent torrent removal is not implemented")
}

func (r *RTorrentClient) DiagnoseLegacy() map[string]interface{} {
	result := r.Diagnose(context.Background())
	return map[string]interface{}{"success": result.Success, "steps": result.Steps}
}

// Diagnose returns the same staged diagnostics shape used by other settings
// integrations, while keeping credentials out of the response.
func (r *RTorrentClient) Diagnose(ctx context.Context) diagnostics.Result {
	return diagnostics.DiagnoseRTorrent(ctx, diagnostics.RTorrentConfig{
		URL: r.cfg.URL, AuthMode: r.cfg.AuthMode, TLSVerify: r.cfg.TLSVerify, Timeout: r.cfg.Timeout,
		Test: func(testCtx context.Context) (diagnostics.RTorrentTestResult, error) {
			info, err := r.TestConnection(testCtx)
			return diagnostics.RTorrentTestResult{Version: info.Version, AuthScheme: info.AuthScheme, DigestAccepted: info.DigestAccepted, HTTPStatus: info.HTTPStatus, LatencyMillis: info.LatencyMillis}, err
		},
	})
}

func (r *RTorrentClient) call(ctx context.Context, method string, params ...rpcValue) (rpcValue, error) {
	value, _, err := r.callDetailed(ctx, method, params...)
	return value, err
}

type rtorrentProbe struct {
	authScheme     string
	digestAccepted bool
	status         int
}

func (r *RTorrentClient) callDetailed(ctx context.Context, method string, params ...rpcValue) (rpcValue, rtorrentProbe, error) {
	authMode := strings.ToLower(strings.TrimSpace(r.cfg.AuthMode))
	if authMode == "" {
		authMode = "auto"
	}
	if authMode != "auto" && authMode != "basic" && authMode != "digest" {
		return rpcValue{}, rtorrentProbe{}, fmt.Errorf("unsupported rTorrent authentication mode")
	}
	if err := netutil.ValidateIntegrationURL(r.cfg.URL); err != nil {
		return rpcValue{}, rtorrentProbe{}, fmt.Errorf("invalid rTorrent URL: %w", err)
	}
	u, err := url.Parse(r.cfg.URL)
	if err != nil || u.User != nil {
		return rpcValue{}, rtorrentProbe{}, fmt.Errorf("rTorrent URL must not contain credentials")
	}
	rpcParams := make([]rpcParam, 0, len(params))
	for _, param := range params {
		rpcParams = append(rpcParams, rpcParam{Value: param})
	}
	body, err := xml.Marshal(rpcRequest{MethodName: method, Params: rpcParams})
	if err != nil {
		return rpcValue{}, rtorrentProbe{}, fmt.Errorf("encode rTorrent XML-RPC request: %w", err)
	}
	requestBody := append([]byte(xml.Header), body...)
	var challenge *digestChallenge
	probe := rtorrentProbe{}
	requestURL := r.cfg.URL
	for attempt := 0; attempt < 3; attempt++ {
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(requestBody))
		if requestErr != nil {
			return rpcValue{}, probe, fmt.Errorf("build rTorrent request: %w", requestErr)
		}
		req.Header.Set("Content-Type", "text/xml")
		switch authMode {
		case "basic":
			if r.cfg.Username != "" {
				req.SetBasicAuth(r.cfg.Username, r.cfg.Password)
			}
		case "digest":
			if challenge != nil {
				req.Header.Set("Authorization", digestAuthorization(challenge, r.cfg.Username, r.cfg.Password, req.Method, req.URL.RequestURI()))
			}
		default:
			if challenge != nil {
				req.Header.Set("Authorization", digestAuthorization(challenge, r.cfg.Username, r.cfg.Password, req.Method, req.URL.RequestURI()))
			} else if r.cfg.Username != "" {
				req.SetBasicAuth(r.cfg.Username, r.cfg.Password)
			}
		}
		resp, requestErr := r.client.Do(req)
		if requestErr != nil {
			return rpcValue{}, probe, fmt.Errorf("rTorrent XML-RPC request failed: %w", requestErr)
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if readErr != nil {
			return rpcValue{}, probe, fmt.Errorf("read rTorrent response: %w", readErr)
		}
		probe.status = resp.StatusCode
		if resp.Request != nil && resp.Request.URL != nil {
			requestURL = resp.Request.URL.String()
		}
		if resp.StatusCode == http.StatusUnauthorized {
			if authMode == "basic" || r.cfg.Username == "" {
				return rpcValue{}, probe, fmt.Errorf("rTorrent authentication rejected")
			}
			parsed, parseErr := parseDigestChallenge(resp.Header.Get("WWW-Authenticate"))
			if parseErr != nil {
				return rpcValue{}, probe, parseErr
			}
			if challenge != nil && !parsed.stale && parsed.nonce == challenge.nonce {
				return rpcValue{}, probe, fmt.Errorf("rTorrent authentication rejected after Digest challenge")
			}
			challenge = &parsed
			continue
		}
		if resp.StatusCode == http.StatusForbidden {
			return rpcValue{}, probe, fmt.Errorf("rTorrent authentication rejected")
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return rpcValue{}, probe, fmt.Errorf("rTorrent XML-RPC endpoint returned HTTP %d", resp.StatusCode)
		}
		if challenge != nil {
			probe.authScheme = "Digest"
			probe.digestAccepted = true
		} else if r.cfg.Username != "" {
			probe.authScheme = "Basic"
		} else {
			probe.authScheme = "None"
		}
		if strings.Contains(strings.ToLower(string(responseBody)), "<html") {
			return rpcValue{}, probe, fmt.Errorf("rTorrent endpoint returned HTML instead of XML-RPC")
		}
		value, parseErr := parseRPCResponse(responseBody)
		if parseErr != nil {
			return rpcValue{}, probe, parseErr
		}
		return value, probe, nil
	}
	return rpcValue{}, probe, fmt.Errorf("rTorrent authentication rejected after Digest challenge")
}

func sameRTorrentOriginRedirect(endpoint string) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 8 {
			return fmt.Errorf("too many rTorrent redirects")
		}
		base, baseErr := url.Parse(endpoint)
		if baseErr != nil || !sameHTTPOrigin(base, req.URL) {
			return fmt.Errorf("rTorrent redirect rejected: different origin")
		}
		req.Header.Del("Authorization")
		return nil
	}
}

func sameHTTPOrigin(left, right *url.URL) bool {
	if left == nil || right == nil || !strings.EqualFold(left.Scheme, right.Scheme) || !strings.EqualFold(left.Hostname(), right.Hostname()) {
		return false
	}
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if strings.EqualFold(u.Scheme, "https") {
			return "443"
		}
		return "80"
	}
	return port(left) == port(right)
}

func normalizeRTorrentConfig(cfg RTorrentConfig) (RTorrentConfig, error) {
	if strings.TrimSpace(cfg.URL) != "" && strings.TrimSpace(cfg.Host) == "" {
		u, err := url.Parse(strings.TrimSpace(cfg.URL))
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return cfg, fmt.Errorf("invalid rTorrent URL")
		}
		cfg.Host, cfg.URLPath, cfg.UseTLS = u.Hostname(), u.EscapedPath(), u.Scheme == "https"
		if cfg.Port == 0 {
			if p := u.Port(); p != "" {
				_, _ = fmt.Sscanf(p, "%d", &cfg.Port)
			} else if cfg.UseTLS {
				cfg.Port = 443
			} else {
				cfg.Port = 80
			}
		}
		return cfg, nil
	}
	if strings.TrimSpace(cfg.Host) == "" {
		return cfg, nil
	}
	if cfg.URLPath == "" {
		cfg.URLPath = "/rutorrent/plugins/httprpc/action.php"
	}
	if !strings.HasPrefix(cfg.URLPath, "/") {
		cfg.URLPath = "/" + cfg.URLPath
	}
	if cfg.Port == 0 {
		if cfg.UseTLS {
			cfg.Port = 443
		} else {
			cfg.Port = 80
		}
	}
	scheme := "http"
	if cfg.UseTLS {
		scheme = "https"
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return cfg, fmt.Errorf("rTorrent port must be between 1 and 65535")
	}
	cfg.URL = fmt.Sprintf("%s://%s%s", scheme, net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)), cfg.URLPath)
	return cfg, nil
}

// RTorrentEndpoint returns the effective XML-RPC endpoint while accepting both
// the current host/port/path fields and the legacy full URL.
func RTorrentEndpoint(cfg RTorrentConfig) (string, error) {
	normalized, err := normalizeRTorrentConfig(cfg)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(normalized.URL) == "" {
		return "", fmt.Errorf("rTorrent endpoint is not configured")
	}
	return normalized.URL, nil
}

// RTorrentEndpointFields returns the effective UI fields, including values
// migrated from a legacy full URL.
func RTorrentEndpointFields(cfg RTorrentConfig) (host string, port int, useTLS bool, path string, err error) {
	normalized, err := normalizeRTorrentConfig(cfg)
	if err != nil {
		return "", 0, false, "", err
	}
	u, err := url.Parse(normalized.URL)
	if err != nil || u.Hostname() == "" {
		return "", 0, false, "", fmt.Errorf("rTorrent endpoint is not configured")
	}
	port = normalized.Port
	if port == 0 {
		if parsedPort := u.Port(); parsedPort != "" {
			_, _ = fmt.Sscanf(parsedPort, "%d", &port)
		} else if u.Scheme == "https" {
			port = 443
		} else {
			port = 80
		}
	}
	return u.Hostname(), port, u.Scheme == "https", u.EscapedPath(), nil
}

func mapRTorrentState(active bool, progress float64) string {
	if progress >= 1 {
		return "completed"
	}
	if active {
		return "downloading"
	}
	return "stopped"
}

type rpcRequest struct {
	XMLName    xml.Name   `xml:"methodCall"`
	MethodName string     `xml:"methodName"`
	Params     []rpcParam `xml:"params>param"`
}

type rpcParam struct {
	Value rpcValue `xml:"value"`
}

type rpcValue struct {
	XMLName    xml.Name
	String     *string `xml:"string"`
	Int        *int64  `xml:"int"`
	I4         *int64  `xml:"i4"`
	Bool       *int64  `xml:"boolean"`
	Base64     *string `xml:"base64"`
	ArrayValue *struct {
		Values []rpcValue `xml:"data>value"`
	} `xml:"array"`
}

func (v rpcValue) Array() ([]rpcValue, bool) {
	if v.ArrayValue == nil {
		return nil, false
	}
	return v.ArrayValue.Values, true
}

func stringPtr(value string) *string { return &value }

func valueString(v rpcValue) string {
	if v.String != nil {
		return *v.String
	}
	if v.Int != nil {
		return fmt.Sprintf("%d", *v.Int)
	}
	if v.I4 != nil {
		return fmt.Sprintf("%d", *v.I4)
	}
	if v.Bool != nil {
		return fmt.Sprintf("%d", *v.Bool)
	}
	return ""
}

func valueInt(v rpcValue) int64 {
	if v.Int != nil {
		return *v.Int
	}
	if v.I4 != nil {
		return *v.I4
	}
	if v.Bool != nil {
		return *v.Bool
	}
	return 0
}

func parseRPCResponse(body []byte) (rpcValue, error) {
	var response struct {
		Fault  *rpcValue  `xml:"fault>value"`
		Params []rpcParam `xml:"params>param"`
	}
	if err := xml.Unmarshal(body, &response); err != nil {
		return rpcValue{}, fmt.Errorf("rTorrent returned invalid XML-RPC: %w", err)
	}
	if response.Fault != nil {
		return rpcValue{}, fmt.Errorf("rTorrent XML-RPC fault: %s", valueString(*response.Fault))
	}
	if len(response.Params) != 1 {
		return rpcValue{}, fmt.Errorf("rTorrent XML-RPC response contained no value")
	}
	return response.Params[0].Value, nil
}
