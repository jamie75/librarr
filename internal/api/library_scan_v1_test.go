package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/librarr/internal/config"
	"github.com/JeremiahM37/librarr/internal/db"
	"github.com/JeremiahM37/librarr/internal/library"
	libraryimport "github.com/JeremiahM37/librarr/internal/library/import"
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

func TestV1LibraryScanPrettyResults(t *testing.T) {
	s, cleanup := newLibraryScanAPIServer(t)
	defer cleanup()
	root := filepath.Join(t.TempDir(), "ebooks")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Debug Book.epub"), []byte("bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	settings := map[string]interface{}{
		"ebook_dir":     root,
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
	waitAPIScanJob(t, s, started.JobID)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/library/scan/"+started.JobID+"/results?pretty=1", nil)
	req.SetPathValue("job_id", started.JobID)
	req.Header.Set("X-Api-Key", s.cfg.APIKey)
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/html; charset=UTF-8" {
		t.Fatalf("content type = %q", ct)
	}
	if body := rr.Body.String(); !strings.Contains(body, "Classification") || !strings.Contains(body, "Debug Book") {
		t.Fatalf("body = %s", body)
	}
}

func TestV1LibraryImportSelectedUpdatesScanReview(t *testing.T) {
	engine := &sequenceImportEngine{results: []*libraryimport.EngineResult{{InsertedCount: 1}}}
	s, cleanup := newLibraryScanAPIServerWithEngine(t, engine)
	defer cleanup()
	scanJobID := startCompletedAPIScan(t, s, []string{"Import Me.epub"})
	result := getAPIScanResult(t, s, scanJobID)

	importJobID := startAPILibraryImport(t, s, map[string]interface{}{
		"scan_job_id":   scanJobID,
		"candidate_ids": []string{result.Candidates[0].ID},
		"all_ready":     false,
	})
	importJob := waitAPIImportJob(t, s, importJobID)
	if importJob.Status != libraryImportStatusCompleted {
		t.Fatalf("job = %+v", importJob)
	}
	importResult := getAPIImportResult(t, s, importJobID)
	if importResult.Summary["imported"] != 1 || len(engine.requests) != 1 {
		t.Fatalf("result=%+v requests=%d", importResult, len(engine.requests))
	}
	updated := getAPIScanResult(t, s, scanJobID)
	if updated.Totals.AlreadyImported != 1 || updated.Totals.ReadyToImport != 0 {
		t.Fatalf("updated scan = %+v", updated.Totals)
	}
}

func TestV1LibraryImportAllReadyExcludesDuplicates(t *testing.T) {
	engine := &sequenceImportEngine{results: []*libraryimport.EngineResult{{InsertedCount: 1}}}
	s, cleanup := newLibraryScanAPIServerWithEngine(t, engine)
	defer cleanup()
	scanJobID := startCompletedAPIScan(t, s, []string{"Ready.epub", "Duplicate.epub"})
	result := getAPIScanResult(t, s, scanJobID)
	var duplicateID string
	for _, candidate := range result.Candidates {
		if filepath.Base(candidate.Path) == "Duplicate.epub" {
			duplicateID = candidate.ID
		}
	}
	if duplicateID == "" {
		t.Fatalf("duplicate candidate missing: %+v", result.Candidates)
	}
	s.libraryScanner.UpdateCandidates(scanJobID, []libraryscanner.CandidateUpdate{{
		ID:                   duplicateID,
		Classification:       libraryscanner.ClassificationDuplicate,
		ClassificationReason: "test duplicate",
	}})

	importJobID := startAPILibraryImport(t, s, map[string]interface{}{
		"scan_job_id": scanJobID,
		"all_ready":   true,
	})
	waitAPIImportJob(t, s, importJobID)
	if len(engine.requests) != 1 || filepath.Base(engine.requests[0].RootPath) != "Ready.epub" {
		t.Fatalf("requests = %+v", engine.requests)
	}
}

func TestV1LibraryScanResolveManualReviewCandidate(t *testing.T) {
	s, cleanup := newLibraryScanAPIServer(t)
	defer cleanup()
	scanJobID := startCompletedAPIScan(t, s, []string{"Review.epub"})
	result := getAPIScanResult(t, s, scanJobID)
	candidateID := result.Candidates[0].ID
	s.libraryScanner.UpdateCandidates(scanJobID, []libraryscanner.CandidateUpdate{{
		ID:                   candidateID,
		Classification:       libraryscanner.ClassificationManualReview,
		ClassificationReason: "Existing title match did not cleanly agree on author",
	}})

	body, _ := json.Marshal(map[string]interface{}{
		"id":     candidateID,
		"action": "edit_metadata",
		"title":  "Resolved Title",
		"author": "Resolved Author",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/library/scan/"+scanJobID+"/resolve", bytes.NewReader(body))
	req.SetPathValue("job_id", scanJobID)
	req.Header.Set("X-Api-Key", s.cfg.APIKey)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("resolve status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resolved libraryscanner.Result
	if err := json.Unmarshal(rr.Body.Bytes(), &resolved); err != nil {
		t.Fatal(err)
	}
	got := resolved.Candidates[0]
	if got.Classification != libraryscanner.ClassificationNew || got.Title != "Resolved Title" || got.Author != "Resolved Author" || got.Metadata.Source != "manual_edit" {
		t.Fatalf("resolved candidate = %+v", got)
	}
	if resolved.Totals.ReadyToImport != 1 || resolved.Totals.ManualReview != 0 {
		t.Fatalf("totals = %+v", resolved.Totals)
	}
}

func TestV1LibraryImportPartialFailureContinues(t *testing.T) {
	engine := &sequenceImportEngine{
		results: []*libraryimport.EngineResult{{InsertedCount: 1}},
		errors:  []error{nil, os.ErrNotExist},
	}
	s, cleanup := newLibraryScanAPIServerWithEngine(t, engine)
	defer cleanup()
	scanJobID := startCompletedAPIScan(t, s, []string{"Good.epub", "Missing.epub"})

	importJobID := startAPILibraryImport(t, s, map[string]interface{}{
		"scan_job_id": scanJobID,
		"all_ready":   true,
	})
	waitAPIImportJob(t, s, importJobID)
	result := getAPIImportResult(t, s, importJobID)
	if result.Summary["imported"] != 1 || result.Summary["failed"] != 1 || len(engine.requests) != 2 {
		t.Fatalf("result=%+v requests=%d", result, len(engine.requests))
	}
	updated := getAPIScanResult(t, s, scanJobID)
	if updated.Totals.AlreadyImported != 1 || updated.Totals.ReadyToImport != 1 {
		t.Fatalf("updated totals = %+v", updated.Totals)
	}
}

func TestV1LibraryImportRejectsConcurrentJob(t *testing.T) {
	engine := &blockingImportEngine{started: make(chan struct{}), release: make(chan struct{})}
	s, cleanup := newLibraryScanAPIServerWithEngine(t, engine)
	defer cleanup()
	scanJobID := startCompletedAPIScan(t, s, []string{"One.epub"})

	startAPILibraryImport(t, s, map[string]interface{}{"scan_job_id": scanJobID, "all_ready": true})
	<-engine.started
	body, _ := json.Marshal(map[string]interface{}{"scan_job_id": scanJobID, "all_ready": true})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/library/import", bytes.NewReader(body))
	req.Header.Set("X-Api-Key", s.cfg.APIKey)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	close(engine.release)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func newLibraryScanAPIServer(t *testing.T) (*Server, func()) {
	return newLibraryScanAPIServerWithEngine(t, nil)
}

func newLibraryScanAPIServerWithEngine(t *testing.T, engine libraryimport.ImportEngine) (*Server, func()) {
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
		importEngine:   engine,
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

func startCompletedAPIScan(t *testing.T, s *Server, filenames []string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "ebooks")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, filename := range filenames {
		if err := os.WriteFile(filepath.Join(root, filename), []byte(filename), 0600); err != nil {
			t.Fatal(err)
		}
	}
	settings := map[string]interface{}{
		"ebook_dir":     root,
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
		t.Fatalf("scan status = %d body=%s", rr.Code, rr.Body.String())
	}
	var started struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	waitAPIScanJob(t, s, started.JobID)
	return started.JobID
}

func getAPIScanResult(t *testing.T, s *Server, jobID string) libraryscanner.Result {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/library/scan/"+jobID+"/results", nil)
	req.SetPathValue("job_id", jobID)
	req.Header.Set("X-Api-Key", s.cfg.APIKey)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("scan result status = %d body=%s", rr.Code, rr.Body.String())
	}
	var result libraryscanner.Result
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func startAPILibraryImport(t *testing.T, s *Server, payload map[string]interface{}) string {
	t.Helper()
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/library/import", bytes.NewReader(body))
	req.Header.Set("X-Api-Key", s.cfg.APIKey)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("import status = %d body=%s", rr.Code, rr.Body.String())
	}
	var started struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	return started.JobID
}

func waitAPIImportJob(t *testing.T, s *Server, jobID string) *libraryImportJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/library/import/"+jobID, nil)
		req.SetPathValue("job_id", jobID)
		req.Header.Set("X-Api-Key", s.cfg.APIKey)
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("import job status = %d body=%s", rr.Code, rr.Body.String())
		}
		var job libraryImportJob
		if err := json.Unmarshal(rr.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		if job.Status == libraryImportStatusCompleted || job.Status == libraryImportStatusFailed {
			return &job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for import %s", jobID)
	return nil
}

func getAPIImportResult(t *testing.T, s *Server, jobID string) libraryImportResult {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/library/import/"+jobID+"/results", nil)
	req.SetPathValue("job_id", jobID)
	req.Header.Set("X-Api-Key", s.cfg.APIKey)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("import result status = %d body=%s", rr.Code, rr.Body.String())
	}
	var result libraryImportResult
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

type sequenceImportEngine struct {
	requests []libraryimport.ImportRequest
	results  []*libraryimport.EngineResult
	errors   []error
}

func (e *sequenceImportEngine) Import(_ context.Context, request libraryimport.ImportRequest) (*libraryimport.EngineResult, error) {
	e.requests = append(e.requests, request)
	idx := len(e.requests) - 1
	if idx < len(e.errors) && e.errors[idx] != nil {
		return nil, e.errors[idx]
	}
	if idx < len(e.results) && e.results[idx] != nil {
		return e.results[idx], nil
	}
	return &libraryimport.EngineResult{InsertedCount: 1}, nil
}

type blockingImportEngine struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *blockingImportEngine) Import(_ context.Context, request libraryimport.ImportRequest) (*libraryimport.EngineResult, error) {
	e.once.Do(func() { close(e.started) })
	<-e.release
	return &libraryimport.EngineResult{InsertedCount: 1}, nil
}
