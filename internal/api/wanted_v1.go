package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jamie75/librarr/internal/db"
	"github.com/jamie75/librarr/internal/library"
	"github.com/jamie75/librarr/internal/models"
	wantedmeta "github.com/jamie75/librarr/internal/wanted"
)

type wantedListResponse struct {
	Items  []models.WantedBook `json:"items"`
	Counts map[string]int      `json:"counts"`
}

type wantedHistoryResponse struct {
	Items []models.WantedSearchHistory `json:"items"`
}

type wantedCreateRequest struct {
	Title              string `json:"title"`
	Author             string `json:"author"`
	ISBN               string `json:"isbn"`
	ASIN               string `json:"asin"`
	Series             string `json:"series"`
	Publisher          string `json:"publisher"`
	Language           string `json:"language"`
	CoverURL           string `json:"cover_url"`
	Description        string `json:"description"`
	Source             string `json:"source"`
	MediaType          string `json:"media_type"`
	Format             string `json:"format"`
	PreferredFormat    string `json:"preferred_format"`
	OriginSource       string `json:"origin_source"`
	OriginReleaseTitle string `json:"origin_release_title"`
	OriginIndexer      string `json:"origin_indexer"`
	Indexer            string `json:"indexer"`
	SourceID           string `json:"source_id"`
	GUID               string `json:"guid"`
	Monitored          *bool  `json:"monitored"`
	Status             string `json:"status"`
}

type wantedPatchRequest struct {
	Monitored *bool   `json:"monitored"`
	Status    *string `json:"status"`
}

func (s *Server) handleV1WantedList(w http.ResponseWriter, r *http.Request) {
	items, err := s.db.ListWantedBooks()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": "Failed to load wanted books"})
		return
	}

	counts := map[string]int{
		"total":      len(items),
		"wanted":     0,
		"ignored":    0,
		"downloaded": 0,
		"found":      0,
		"missing":    0,
		"searching":  0,
		"imported":   0,
		"monitored":  0,
	}
	for _, item := range items {
		counts[strings.TrimSpace(strings.ToLower(item.Status))]++
		if item.Monitored {
			counts["monitored"]++
		}
	}
	writeJSON(w, http.StatusOK, wantedListResponse{Items: items, Counts: counts})
}

func (s *Server) handleV1WantedCreate(w http.ResponseWriter, r *http.Request) {
	var req wantedCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Invalid request body"})
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Title is required"})
		return
	}

	monitored := true
	if req.Monitored != nil {
		monitored = *req.Monitored
	}
	status := strings.TrimSpace(strings.ToLower(req.Status))
	if status == "" {
		status = "wanted"
	}
	if !isValidWantedStatus(status) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Invalid wanted status"})
		return
	}

	book := models.WantedBook{
		Title:              req.Title,
		Author:             req.Author,
		ISBN:               req.ISBN,
		ASIN:               req.ASIN,
		Series:             req.Series,
		Publisher:          req.Publisher,
		Language:           req.Language,
		CoverURL:           req.CoverURL,
		Description:        req.Description,
		Source:             req.Source,
		MediaType:          req.MediaType,
		PreferredFormat:    wantedFirstNonBlank(req.PreferredFormat, req.Format),
		OriginSource:       wantedFirstNonBlank(req.OriginSource, req.Source),
		OriginReleaseTitle: req.OriginReleaseTitle,
		OriginIndexer:      wantedFirstNonBlank(req.OriginIndexer, req.Indexer),
		SourceID:           wantedFirstNonBlank(req.SourceID, req.GUID),
		Monitored:          monitored,
		Status:             status,
	}
	if strings.TrimSpace(book.OriginReleaseTitle) == "" && looksWantedReleaseSource(book.Source, book.OriginSource) {
		book.OriginReleaseTitle = book.Title
	}
	normalization := wantedmeta.NormalizeBook(book)
	if strings.TrimSpace(normalization.Normalized.Title) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Title is required"})
		return
	}
	if conflicts, err := s.wantedCanonicalConflict(0, normalization.Normalized); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": "Failed to validate wanted book"})
		return
	} else if conflicts {
		writeJSON(w, http.StatusConflict, map[string]any{"success": false, "error": "Wanted book already exists"})
		return
	}

	item, err := s.db.CreateWantedBook(normalization.Normalized)
	if err != nil {
		if errors.Is(err, db.ErrWantedBookExists) {
			writeJSON(w, http.StatusConflict, map[string]any{"success": false, "error": "Wanted book already exists"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": "Failed to create wanted book"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "item": item})
}

func (s *Server) handleV1WantedDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Invalid wanted id"})
		return
	}
	if err := s.db.DeleteWantedBook(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": "Wanted book not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": "Failed to delete wanted book"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Server) handleV1WantedPatch(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Invalid wanted id"})
		return
	}

	var req wantedPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Invalid request body"})
		return
	}
	if req.Monitored == nil && req.Status == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "No wanted fields provided"})
		return
	}
	if req.Status != nil && !isValidWantedStatus(strings.TrimSpace(strings.ToLower(*req.Status))) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Invalid wanted status"})
		return
	}

	item, err := s.db.UpdateWantedBook(id, req.Monitored, req.Status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": "Wanted book not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": "Failed to update wanted book"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "item": item})
}

func (s *Server) handleV1WantedNormalize(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Invalid wanted id"})
		return
	}
	current, err := s.db.GetWantedBook(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": "Wanted book not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": "Failed to load wanted book"})
		return
	}
	if !wantedmeta.LooksMalformed(*current) {
		result := wantedmeta.NormalizeBook(*current)
		result.Warnings = append(result.Warnings, "metadata is ambiguous; no changes were applied")
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"success": false, "normalization": result, "error": "Wanted metadata is ambiguous"})
		return
	}
	result := wantedmeta.NormalizeBook(*current)
	if len(result.ChangedFields) == 0 {
		result.Applied = false
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "normalization": result, "item": current})
		return
	}
	if conflicts, err := s.wantedCanonicalConflict(id, result.Normalized); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": "Failed to validate wanted metadata", "normalization": result})
		return
	} else if conflicts {
		writeJSON(w, http.StatusConflict, map[string]any{"success": false, "error": "Normalized wanted book already exists", "normalization": result})
		return
	}
	updated, err := s.db.UpdateWantedBookMetadata(id, result.Normalized)
	if err != nil {
		if errors.Is(err, db.ErrWantedBookExists) {
			writeJSON(w, http.StatusConflict, map[string]any{"success": false, "error": "Normalized wanted book already exists", "normalization": result})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": "Failed to normalize wanted metadata"})
		return
	}
	result.Normalized = *updated
	result.Applied = true
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "normalization": result, "item": updated})
}

func looksWantedReleaseSource(values ...string) bool {
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if strings.Contains(value, "torrent") || strings.Contains(value, "prowlarr") || strings.Contains(value, "release") {
			return true
		}
	}
	return false
}

func wantedFirstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (s *Server) wantedCanonicalConflict(ignoreID int64, candidate models.WantedBook) (bool, error) {
	items, err := s.db.ListWantedBooks()
	if err != nil {
		return false, err
	}
	candidateISBN := strings.ToLower(strings.TrimSpace(candidate.ISBN))
	candidateASIN := strings.ToLower(strings.TrimSpace(candidate.ASIN))
	candidateTitle := library.TitleMatchKey(candidate.Title)
	candidateAuthor := library.ContributorMatchKey(candidate.Author)
	candidateMedia := strings.ToLower(strings.TrimSpace(candidate.MediaType))
	if candidateMedia == "" {
		candidateMedia = "ebook"
	}
	for _, item := range items {
		if item.ID == ignoreID {
			continue
		}
		itemMedia := strings.ToLower(strings.TrimSpace(item.MediaType))
		if itemMedia == "" {
			itemMedia = "ebook"
		}
		if itemMedia != candidateMedia {
			continue
		}
		if candidateISBN != "" && strings.EqualFold(strings.TrimSpace(item.ISBN), candidateISBN) {
			return true, nil
		}
		if candidateASIN != "" && strings.EqualFold(strings.TrimSpace(item.ASIN), candidateASIN) {
			return true, nil
		}
		if candidateTitle != "" && candidateTitle == library.TitleMatchKey(item.Title) &&
			candidateAuthor != "" && candidateAuthor == library.ContributorMatchKey(item.Author) {
			return true, nil
		}
	}
	return false, nil
}

func isValidWantedStatus(status string) bool {
	switch status {
	case "wanted", "searching", "found", "missing", "downloaded", "ignored", "imported":
		return true
	default:
		return false
	}
}

func (s *Server) handleV1WantedHistory(w http.ResponseWriter, r *http.Request) {
	items, err := s.db.ListWantedSearchHistory(100)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": "Failed to load wanted search history"})
		return
	}
	writeJSON(w, http.StatusOK, wantedHistoryResponse{Items: items})
}

func (s *Server) handleV1WantedSearchAll(w http.ResponseWriter, r *http.Request) {
	if s.wantedMonitor == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "error": "Wanted monitor is unavailable"})
		return
	}
	summary, err := s.wantedMonitor.SearchAll(r.Context(), true)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(strings.ToLower(err.Error()), "already running") {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]any{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "summary": summary, "items": summary.Updated})
}

func (s *Server) handleV1WantedSearchOne(w http.ResponseWriter, r *http.Request) {
	if s.wantedMonitor == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "error": "Wanted monitor is unavailable"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Invalid wanted id"})
		return
	}
	item, err := s.wantedMonitor.SearchOne(r.Context(), id)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(strings.ToLower(err.Error()), "already running") {
			status = http.StatusConflict
		}
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "item": item})
}
