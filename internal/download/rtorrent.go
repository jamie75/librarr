package download

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jamie75/librarr/internal/diagnostics"
	"github.com/jamie75/librarr/internal/netutil"
)

// RTorrentConfig contains only read-only connection settings for Phase 1.
type RTorrentConfig struct {
	Name       string
	URL        string
	Username   string
	Password   string
	Timeout    time.Duration
	LabelField string
	TLSVerify  bool
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
	Version string `json:"version,omitempty"`
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

// RTorrentClient is a read-only XML-RPC adapter. It talks to rTorrent's RPC
// endpoint directly and never scrapes or automates ruTorrent.
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
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: !cfg.TLSVerify} // #nosec G402 -- explicit admin setting
	return &RTorrentClient{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout, Transport: transport}}
}

func (r *RTorrentClient) ClientID() string { return "rtorrent" }
func (r *RTorrentClient) Name() string     { return r.cfg.Name }
func (r *RTorrentClient) Type() string     { return "rtorrent" }

func (r *RTorrentClient) TestConnection(ctx context.Context) (ClientInfo, error) {
	version, err := r.call(ctx, "system.client_version")
	if err != nil {
		return ClientInfo{}, err
	}
	return ClientInfo{Version: valueString(version)}, nil
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

// Diagnose returns the same staged diagnostics shape used by other settings
// integrations, while keeping credentials out of the response.
func (r *RTorrentClient) Diagnose(ctx context.Context) diagnostics.Result {
	return diagnostics.DiagnoseRTorrent(ctx, diagnostics.RTorrentConfig{
		URL: r.cfg.URL, Username: r.cfg.Username, Password: r.cfg.Password, Timeout: r.cfg.Timeout,
		Test: func(testCtx context.Context) (string, error) {
			info, err := r.TestConnection(testCtx)
			return info.Version, err
		},
	})
}

func (r *RTorrentClient) call(ctx context.Context, method string, params ...rpcValue) (rpcValue, error) {
	if err := netutil.ValidateIntegrationURL(r.cfg.URL); err != nil {
		return rpcValue{}, fmt.Errorf("invalid rTorrent URL: %w", err)
	}
	u, err := url.Parse(r.cfg.URL)
	if err != nil || u.User != nil {
		return rpcValue{}, fmt.Errorf("rTorrent URL must not contain credentials")
	}
	rpcParams := make([]rpcParam, 0, len(params))
	for _, param := range params {
		rpcParams = append(rpcParams, rpcParam{Value: param})
	}
	body, err := xml.Marshal(rpcRequest{MethodName: method, Params: rpcParams})
	if err != nil {
		return rpcValue{}, fmt.Errorf("encode rTorrent XML-RPC request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.URL, bytes.NewReader(append([]byte(xml.Header), body...)))
	if err != nil {
		return rpcValue{}, fmt.Errorf("build rTorrent request: %w", err)
	}
	req.Header.Set("Content-Type", "text/xml")
	if r.cfg.Username != "" {
		req.SetBasicAuth(r.cfg.Username, r.cfg.Password)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return rpcValue{}, fmt.Errorf("rTorrent XML-RPC request failed: %w", err)
	}
	defer resp.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if readErr != nil {
		return rpcValue{}, fmt.Errorf("read rTorrent response: %w", readErr)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return rpcValue{}, fmt.Errorf("rTorrent authentication rejected")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return rpcValue{}, fmt.Errorf("rTorrent XML-RPC endpoint returned HTTP %d", resp.StatusCode)
	}
	if strings.Contains(strings.ToLower(string(responseBody)), "<html") {
		return rpcValue{}, fmt.Errorf("rTorrent endpoint returned HTML instead of XML-RPC")
	}
	value, err := parseRPCResponse(responseBody)
	if err != nil {
		return rpcValue{}, err
	}
	return value, nil
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
