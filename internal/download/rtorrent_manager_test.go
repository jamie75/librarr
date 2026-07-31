package download

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jamie75/librarr/internal/config"
	"github.com/jamie75/librarr/internal/db"
	"github.com/jamie75/librarr/internal/models"
	"github.com/jamie75/librarr/internal/search"
)

type managerListingClient struct{}

func (managerListingClient) AddTorrent(string, string, string, string, string) error { return nil }
func (managerListingClient) GetTorrents(category string) ([]TorrentInfo, error) {
	if category != "librarr" {
		return nil, nil
	}
	return []TorrentInfo{{Hash: "qb-hash", Name: "qB Book", State: "downloading", Progress: .25, TotalSize: 100}}, nil
}
func (managerListingClient) GetTorrentFiles(string) ([]TorrentFile, error) { return nil, nil }
func (managerListingClient) DeleteTorrent(string, bool) error              { return nil }
func (managerListingClient) Name() string                                  { return "qbittorrent" }

func TestGetDownloadsKeepsActiveQBittorrentRows(t *testing.T) {
	database, err := db.New(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cfg := &config.Config{QBCategory: "librarr"}
	manager := NewManager(cfg, database, managerListingClient{}, nil, nil, nil, nil, search.NewHealthTracker(3, 300))
	got := manager.GetDownloads()
	if len(got) != 1 || got[0].Hash != "qb-hash" || got[0].Status != "downloading" || got[0].Progress != 25 {
		t.Fatalf("downloads = %+v", got)
	}
}

func TestGetDownloadsIncludesTrackedStatesAndExpiresOldImports(t *testing.T) {
	database, err := db.New(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Now().UTC()
	oldImported := now.Add(-48 * time.Hour)
	items := []models.TrackedDownload{
		{ID: "rtorrent:waiting", ClientID: "rtorrent", ClientType: "rtorrent", InfoHash: "waiting", Title: "Waiting Book", Status: "completed", ImportStatus: "pending", RemotePath: "/remote/book.epub", CreatedAt: now},
		{ID: "rtorrent:importing", ClientID: "rtorrent", ClientType: "rtorrent", InfoHash: "importing", Title: "Importing Book", Status: "completed", ImportStatus: "importing", CreatedAt: now},
		{ID: "rtorrent:failed", ClientID: "rtorrent", ClientType: "rtorrent", InfoHash: "failed", Title: "Failed Book", Status: "completed", ImportStatus: "failed", LastError: "permission denied", CreatedAt: now},
		{ID: "rtorrent:recent", ClientID: "rtorrent", ClientType: "rtorrent", InfoHash: "recent", Title: "Recent Book", Status: "imported", ImportStatus: "imported", ImportedAt: &now, CreatedAt: now},
		{ID: "rtorrent:old", ClientID: "rtorrent", ClientType: "rtorrent", InfoHash: "old", Title: "Old Book", Status: "imported", ImportStatus: "imported", ImportedAt: &oldImported, CreatedAt: oldImported},
	}
	for i := range items {
		if err := database.SaveTrackedDownload(&items[i]); err != nil {
			t.Fatal(err)
		}
	}
	manager := NewManager(&config.Config{}, database, nil, nil, nil, nil, nil, search.NewHealthTracker(3, 300))
	got := manager.GetDownloads()
	byHash := make(map[string]models.DownloadStatus, len(got))
	for _, item := range got {
		byHash[item.Hash] = item
	}
	for hash, wantStatus := range map[string]string{"waiting": "waiting", "importing": "importing", "failed": "failed", "recent": "imported"} {
		if byHash[hash].Status != wantStatus {
			t.Fatalf("hash %q status = %q, want %q; downloads=%+v", hash, byHash[hash].Status, wantStatus, got)
		}
	}
	if _, ok := byHash["old"]; ok {
		t.Fatalf("old imported row should age out: %+v", got)
	}
}

func TestRTorrentFailedSubmissionDoesNotCreateTrackedRow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, rpcFaultResponse(-503, "Too few arguments."))
	}))
	defer srv.Close()
	database, err := db.New(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	client := newTestRTorrentClient(RTorrentConfig{URL: srv.URL, Timeout: time.Second})
	manager := NewManager(&config.Config{}, database, client, nil, nil, nil, nil, search.NewHealthTracker(3, 300))
	if _, err := manager.StartTorrentDownloadTracked("magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01", "Book", "/remote", "librarr", "", "ebook", "", ""); err == nil {
		t.Fatal("expected submission failure")
	}
	items, err := database.GetTrackedDownloads()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("tracked rows=%d, want zero", len(items))
	}
}

func TestRTorrentSuccessfulSubmissionCreatesTrackedRow(t *testing.T) {
	hash := "abcdef0123456789abcdef0123456789abcdef01"
	srv := rtorrentServer(t, func(method string) string {
		if method == "load.start" {
			return rpcStringResponse(hash)
		}
		return rpcStringResponse("")
	})
	defer srv.Close()
	database, err := db.New(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	client := newTestRTorrentClient(RTorrentConfig{URL: srv.URL, Timeout: time.Second})
	manager := NewManager(&config.Config{}, database, client, nil, nil, nil, nil, search.NewHealthTracker(3, 300))
	if _, err := manager.StartTorrentDownloadTracked("magnet:?xt=urn:btih:"+hash, "Book", "/remote", "librarr", "", "ebook", "torrent", "source-1"); err != nil {
		t.Fatalf("submission error: %v", err)
	}
	items, err := database.GetTrackedDownloads()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].InfoHash != hash || items[0].RemoteSavePath != "/remote" {
		t.Fatalf("tracked rows=%+v", items)
	}
}
