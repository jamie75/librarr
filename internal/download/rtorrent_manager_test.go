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
	"github.com/jamie75/librarr/internal/search"
)

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
	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, Timeout: time.Second})
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
	client := NewRTorrentClient(RTorrentConfig{URL: srv.URL, Timeout: time.Second})
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
