package db

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jamie75/librarr/internal/models"
)

func TestTrackedDownloadRoundTrip(t *testing.T) {
	database, err := New(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Now().UTC().Truncate(time.Second)
	want := &models.TrackedDownload{ID: "rtorrent:hash", ClientID: "rtorrent", ClientType: "rtorrent", DownloadID: "hash", InfoHash: "hash", Title: "Book", MediaType: "ebook", Category: "librarr", Status: "submitted", ImportStatus: "pending", CreatedAt: now}
	if err := database.SaveTrackedDownload(want); err != nil {
		t.Fatal(err)
	}
	got, err := database.GetTrackedDownload(want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClientID != want.ClientID || got.InfoHash != want.InfoHash || got.Title != want.Title || got.ImportStatus != want.ImportStatus {
		t.Fatalf("tracked download = %+v", got)
	}
	items, err := database.GetTrackedDownloads()
	if err != nil || len(items) != 1 {
		t.Fatalf("tracked downloads = %+v, err=%v", items, err)
	}
}
