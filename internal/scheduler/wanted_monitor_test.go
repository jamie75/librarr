package scheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jamie75/librarr/internal/config"
	"github.com/jamie75/librarr/internal/db"
	"github.com/jamie75/librarr/internal/models"
)

func newWantedMonitorDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.New(filepath.Join(t.TempDir(), "wanted-monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func TestWantedMonitorSearchOneUpdatesWantedBookAndHistory(t *testing.T) {
	database := newWantedMonitorDB(t)
	defer database.Close()

	book, err := database.CreateWantedBook(models.WantedBook{
		Title:     "The Martian",
		Author:    "Andy Weir",
		MediaType: "ebook",
		Monitored: true,
		Status:    "wanted",
	})
	if err != nil {
		t.Fatal(err)
	}

	var apiKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey = r.Header.Get("X-Api-Key")
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"title":       "The Martian EPUB",
			"downloadUrl": "http://example.test/download",
			"protocol":    "torrent",
			"seeders":     15,
			"infoHash":    "abc123",
		}})
	}))
	defer server.Close()

	cfg := &config.Config{
		ProwlarrURL:          server.URL,
		ProwlarrAPIKey:       "prowlarr-key",
		WantedMaxResultsKeep: 10,
	}
	monitor := NewWantedMonitor(cfg, database, nil, server.Client())

	updated, err := monitor.SearchOne(context.Background(), book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if apiKey != "prowlarr-key" {
		t.Fatalf("api key = %q, want prowlarr-key", apiKey)
	}
	if updated.Status != "found" {
		t.Fatalf("status = %q, want found", updated.Status)
	}
	if updated.LastResultCount != 1 {
		t.Fatalf("last result count = %d, want 1", updated.LastResultCount)
	}
	if updated.LastMatchTitle == "" || updated.LastSearch == nil {
		t.Fatalf("updated wanted book missing search fields: %+v", updated)
	}

	history, err := database.ListWantedSearchHistory(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1", len(history))
	}
	if !history[0].Success || history[0].BestMatchTitle == "" {
		t.Fatalf("history entry = %+v", history[0])
	}
}

func TestWantedMonitorSearchAllSkipsFailedItemsWhenRetryDisabled(t *testing.T) {
	database := newWantedMonitorDB(t)
	defer database.Close()

	first, err := database.CreateWantedBook(models.WantedBook{
		Title:     "Project Hail Mary",
		Author:    "Andy Weir",
		MediaType: "ebook",
		Monitored: true,
		Status:    "wanted",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := database.CreateWantedBook(models.WantedBook{
		Title:     "Artemis",
		Author:    "Andy Weir",
		MediaType: "ebook",
		Monitored: true,
		Status:    "wanted",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.UpdateWantedSearch(first.ID, db.WantedSearchUpdate{
		Status:      "wanted",
		LastSearch:  time.Now().UTC(),
		LastSuccess: false,
		LastError:   "temporary failure",
	})
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"title":       "Artemis EPUB",
			"downloadUrl": "http://example.test/download",
			"protocol":    "torrent",
			"seeders":     10,
			"infoHash":    "hash-artemis",
		}})
	}))
	defer server.Close()

	cfg := &config.Config{
		ProwlarrURL:          server.URL,
		ProwlarrAPIKey:       "prowlarr-key",
		WantedRetryFailures:  false,
		WantedMaxResultsKeep: 10,
	}
	monitor := NewWantedMonitor(cfg, database, nil, server.Client())

	summary, err := monitor.SearchAll(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Searched != 1 {
		t.Fatalf("searched = %d, want 1", summary.Searched)
	}

	refetchedFirst, err := database.GetWantedBook(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refetchedFirst.LastError != "temporary failure" {
		t.Fatalf("first book last error = %q, want preserved failure", refetchedFirst.LastError)
	}
	refetchedSecond, err := database.GetWantedBook(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refetchedSecond.Status != "found" {
		t.Fatalf("second status = %q, want found", refetchedSecond.Status)
	}
}
