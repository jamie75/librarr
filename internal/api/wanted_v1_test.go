package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/jamie75/librarr/internal/config"
	"github.com/jamie75/librarr/internal/db"
	"github.com/jamie75/librarr/internal/library"
	"github.com/jamie75/librarr/internal/models"
	"github.com/jamie75/librarr/internal/scheduler"
)

func newWantedAPIServer(t *testing.T) (*Server, func()) {
	t.Helper()
	database, err := db.New(filepath.Join(t.TempDir(), "wanted.db"))
	if err != nil {
		t.Fatal(err)
	}
	seedCompletedEmptyBackfill(t, database)

	cfg := &config.Config{LibraryRepositoryMode: "normalized"}
	selection, err := library.NewConfiguredLibraryService(context.Background(), cfg, database)
	if err != nil {
		t.Fatal(err)
	}

	return &Server{
		cfg:            cfg,
		db:             database,
		libraryService: selection.LibraryService,
	}, func() { _ = database.Close() }
}

func TestWantedCreateListPatchDelete(t *testing.T) {
	s, cleanup := newWantedAPIServer(t)
	defer cleanup()

	body := bytes.NewBufferString(`{"title":"The Martian","author":"Andy Weir","media_type":"ebook","monitored":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/wanted", body)
	rr := httptest.NewRecorder()
	s.handleV1WantedCreate(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rr.Code, rr.Body.String())
	}
	var created struct {
		Success bool `json:"success"`
		Item    struct {
			ID        int64  `json:"id"`
			Title     string `json:"title"`
			Author    string `json:"author"`
			Monitored bool   `json:"monitored"`
			Status    string `json:"status"`
		} `json:"item"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !created.Success || created.Item.ID == 0 || created.Item.Status != "wanted" || !created.Item.Monitored {
		t.Fatalf("created = %+v", created)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/wanted", nil)
	rr = httptest.NewRecorder()
	s.handleV1WantedList(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rr.Code, rr.Body.String())
	}
	var listed wantedListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 || listed.Counts["wanted"] != 1 || listed.Counts["monitored"] != 1 {
		t.Fatalf("listed = %+v", listed)
	}

	patch := bytes.NewBufferString(`{"monitored":false,"status":"ignored"}`)
	req = httptest.NewRequest(http.MethodPatch, "/api/v1/wanted/1", patch)
	req.SetPathValue("id", "1")
	rr = httptest.NewRecorder()
	s.handleV1WantedPatch(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("patch status = %d body=%s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/wanted", nil)
	rr = httptest.NewRecorder()
	s.handleV1WantedList(rr, req)
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 || listed.Items[0].Monitored || listed.Items[0].Status != "ignored" || listed.Counts["ignored"] != 1 {
		t.Fatalf("updated list = %+v", listed)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/wanted/1", nil)
	req.SetPathValue("id", "1")
	rr = httptest.NewRecorder()
	s.handleV1WantedDelete(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestWantedDuplicateRejected(t *testing.T) {
	s, cleanup := newWantedAPIServer(t)
	defer cleanup()

	body := []byte(`{"title":"Project Hail Mary","author":"Andy Weir","media_type":"ebook"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/wanted", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleV1WantedCreate(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("first create status = %d body=%s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/wanted", bytes.NewReader(body))
	rr = httptest.NewRecorder()
	s.handleV1WantedCreate(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestWantedValidation(t *testing.T) {
	s, cleanup := newWantedAPIServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/wanted", bytes.NewBufferString(`{"author":"Unknown"}`))
	rr := httptest.NewRecorder()
	s.handleV1WantedCreate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing title status = %d body=%s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodPatch, "/api/v1/wanted/12", bytes.NewBufferString(`{"status":"bogus"}`))
	req.SetPathValue("id", "12")
	rr = httptest.NewRecorder()
	s.handleV1WantedPatch(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid patch status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestWantedSearchEndpoints(t *testing.T) {
	database, err := db.New(filepath.Join(t.TempDir(), "wanted-search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	seedCompletedEmptyBackfill(t, database)

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

	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"title":       "The Martian EPUB",
			"downloadUrl": "http://example.test/download",
			"protocol":    "torrent",
			"seeders":     9,
			"infoHash":    "hash-1",
		}})
	}))
	defer prowlarr.Close()

	cfg := &config.Config{
		LibraryRepositoryMode: "normalized",
		ProwlarrURL:           prowlarr.URL,
		ProwlarrAPIKey:        "test-key",
		WantedMaxResultsKeep:  10,
	}
	selection, err := library.NewConfiguredLibraryService(context.Background(), cfg, database)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		cfg:            cfg,
		db:             database,
		libraryService: selection.LibraryService,
		wantedMonitor:  scheduler.NewWantedMonitor(cfg, database, nil, prowlarr.Client()),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/wanted/1/search", nil)
	req.SetPathValue("id", "1")
	rr := httptest.NewRecorder()
	s.handleV1WantedSearchOne(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("search one status = %d body=%s", rr.Code, rr.Body.String())
	}

	refetched, err := database.GetWantedBook(book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refetched.Status != "found" {
		t.Fatalf("status = %q, want found", refetched.Status)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/wanted/history", nil)
	rr = httptest.NewRecorder()
	s.handleV1WantedHistory(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("history status = %d body=%s", rr.Code, rr.Body.String())
	}
	var history wantedHistoryResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Items) != 1 || history.Items[0].WantedBookID != book.ID {
		t.Fatalf("history = %+v", history)
	}
}
