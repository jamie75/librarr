package api

import (
	"errors"
	"net/http"
	"strings"

	libraryscanner "github.com/JeremiahM37/librarr/internal/library/scanner"
)

func (s *Server) handleV1LibraryScanStart(w http.ResponseWriter, r *http.Request) {
	if s.libraryScanner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{"success": false, "error": "Library scanner is not configured"})
		return
	}
	job, err := s.libraryScanner.Start(r.Context(), s.currentLibraryScanRoots())
	if err != nil {
		var active *libraryscanner.ActiveJobError
		if errors.As(err, &active) {
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"success": false,
				"error":   "Library scan already running",
				"job_id":  active.JobID,
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"success": false, "error": "Failed to start library scan"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"success": true,
		"job_id":  job.ID,
		"job":     job,
	})
}

func (s *Server) handleV1LibraryScanJob(w http.ResponseWriter, r *http.Request) {
	if s.libraryScanner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{"success": false, "error": "Library scanner is not configured"})
		return
	}
	jobID := strings.TrimSpace(r.PathValue("job_id"))
	job, ok := s.libraryScanner.Get(jobID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"success": false, "error": "Scan job not found"})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleV1LibraryScanResults(w http.ResponseWriter, r *http.Request) {
	if s.libraryScanner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{"success": false, "error": "Library scanner is not configured"})
		return
	}
	jobID := strings.TrimSpace(r.PathValue("job_id"))
	job, ok := s.libraryScanner.Get(jobID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"success": false, "error": "Scan job not found"})
		return
	}
	if job.Status != libraryscanner.StatusCompleted {
		writeJSON(w, http.StatusConflict, map[string]interface{}{"success": false, "error": "Scan results are not ready", "status": job.Status})
		return
	}
	result, ok := s.libraryScanner.Result(jobID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"success": false, "error": "Scan results not found"})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) currentLibraryScanRoots() libraryscanner.Roots {
	roots := libraryscanner.Roots{
		EbookDir:     s.cfg.EbookDir,
		AudiobookDir: s.cfg.AudiobookDir,
		MangaDir:     s.cfg.MangaDir,
	}
	settings := s.loadSettings()
	if value, ok := settings["ebook_dir"].(string); ok && strings.TrimSpace(value) != "" {
		roots.EbookDir = strings.TrimSpace(value)
	}
	if value, ok := settings["audiobook_dir"].(string); ok && strings.TrimSpace(value) != "" {
		roots.AudiobookDir = strings.TrimSpace(value)
	}
	if value, ok := settings["manga_dir"].(string); ok && strings.TrimSpace(value) != "" {
		roots.MangaDir = strings.TrimSpace(value)
	}
	return roots
}
