package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JeremiahM37/librarr/internal/config"
	"github.com/JeremiahM37/librarr/internal/db"
	"github.com/JeremiahM37/librarr/internal/library"
	libraryscanner "github.com/JeremiahM37/librarr/internal/library/scanner"
)

func TestV1LibraryScanRequiresAuthentication(t *testing.T) {
	s, cleanup := newLibraryScanAPIServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/library/scan", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestV1LibraryScanUsesSavedSettingsAndReturnsResults(t *testing.T) {
	s, cleanup := newLibraryScanAPIServer(t)
	defer cleanup()
	savedRoot := filepath.Join(t.TempDir(), "saved-ebooks")
	if err := os.MkdirAll(savedRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(savedRoot, "Saved Book.epub"), []byte("bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	settings := map[string]interface{}{
		"ebook_dir":     savedRoot,
		"audiobook_dir": filepath.Join(t.TempDir(), "missing-audio"),
		"manga_dir":     filepath.Join(t.TempDir(), "missing-manga"),
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(s.cfg.SettingsFile, data, 0600); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/library/scan", nil)
	req.Header.Set("X-Api-Key", s.cfg.APIKey)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var started struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}

	job := waitAPIScanJob(t, s, started.JobID)
	if job.Status != libraryscanner.StatusCompleted {
		t.Fatalf("job = %+v", job)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/library/scan/"+started.JobID+"/results", nil)
	req.SetPathValue("job_id", started.JobID)
	req.Header.Set("X-Api-Key", s.cfg.APIKey)
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("results status = %d body=%s", rr.Code, rr.Body.String())
	}
	var result libraryscanner.Result
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Totals.ReadyToImport != 1 || result.Candidates[0].Path != filepath.Join(savedRoot, "Saved Book.epub") {
		t.Fatalf("result = %+v", result)
	}
}

func TestV1LibraryScanUnknownJob(t *testing.T) {
	s, cleanup := newLibraryScanAPIServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/library/scan/nope", nil)
	req.SetPathValue("job_id", "nope")
	req.Header.Set("X-Api-Key", s.cfg.APIKey)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func newLibraryScanAPIServer(t *testing.T) (*Server, func()) {
	t.Helper()
	d, err := db.New(filepath.Join(t.TempDir(), "scan-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := library.NewLegacyLibraryService(d)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		APIKey:       "test-api-key",
		SettingsFile: filepath.Join(t.TempDir(), "settings.json"),
		EbookDir:     filepath.Join(t.TempDir(), "default-ebooks"),
		AudiobookDir: filepath.Join(t.TempDir(), "default-audio"),
		MangaDir:     filepath.Join(t.TempDir(), "default-manga"),
	}
	s := &Server{
		cfg:            cfg,
		db:             d,
		mux:            http.NewServeMux(),
		sessions:       NewSessionStore(),
		libraryService: service,
		libraryScanner: libraryscanner.NewManager(service),
	}
	s.registerLibraryRoutes()
	return s, func() { d.Close() }
}

func waitAPIScanJob(t *testing.T, s *Server, jobID string) *libraryscanner.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/library/scan/"+jobID, nil)
		req.SetPathValue("job_id", jobID)
		req.Header.Set("X-Api-Key", s.cfg.APIKey)
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("job status = %d body=%s", rr.Code, rr.Body.String())
		}
		var job libraryscanner.Job
		if err := json.Unmarshal(rr.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		if job.Status == libraryscanner.StatusCompleted || job.Status == libraryscanner.StatusFailed || job.Status == libraryscanner.StatusCancelled {
			return &job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", jobID)
	return nil
}
