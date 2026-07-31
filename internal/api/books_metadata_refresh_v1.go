package api

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/jamie75/librarr/internal/library"
)

func (s *Server) handleV1BookMetadataRefresh(w http.ResponseWriter, r *http.Request) {
	if !s.ensureNormalizedReadAPI(w) {
		return
	}
	bookID, ok := parseIDPathValue(w, r, "id", "Invalid book ID")
	if !ok {
		return
	}
	result, err := s.refreshBookMetadata(r, bookID)
	if errors.Is(err, library.ErrBookNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": "Book not found"})
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "Failed to refresh book metadata", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "result": result})
}

func (s *Server) handleV1LibraryMetadataRefresh(w http.ResponseWriter, r *http.Request) {
	if !s.ensureNormalizedReadAPI(w) {
		return
	}
	report, err := s.library().RefreshAllMetadata(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to refresh library metadata", err)
		return
	}
	for i := range report.Results {
		if report.Results[i].Refreshed {
			s.attachRefreshCover(r, &report.Results[i])
			if report.Results[i].CoverUpdated {
				report.CoversUpdated++
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "report": report})
}

func (s *Server) refreshBookMetadata(r *http.Request, bookID int64) (library.MetadataRefreshResult, error) {
	result, err := s.library().RefreshBookMetadata(r.Context(), bookID)
	if err != nil {
		return result, err
	}
	s.attachRefreshCover(r, &result)
	return result, nil
}

func (s *Server) attachRefreshCover(r *http.Request, result *library.MetadataRefreshResult) {
	if s.coverCache == nil || result == nil || !result.Refreshed {
		return
	}
	files, err := s.library().GetBookFiles(r.Context(), result.BookID)
	if err != nil {
		return
	}
	for _, file := range files {
		if strings.TrimSpace(file.Path) == "" {
			continue
		}
		before, _ := s.library().GetPrimaryCover(r.Context(), result.BookID)
		if _, err := s.coverCache.AttachBookCover(r.Context(), s.library(), result.BookID, file.Path); err != nil {
			continue
		}
		after, _ := s.library().GetPrimaryCover(r.Context(), result.BookID)
		if !coverExists(before) && coverExists(after) {
			result.CoverUpdated = true
		}
		break
	}
}

func coverExists(cover *library.Cover) bool {
	if cover == nil {
		return false
	}
	if strings.TrimSpace(cover.SourceURL) != "" {
		return true
	}
	info, err := os.Stat(strings.TrimSpace(cover.LocalPath))
	return err == nil && !info.IsDir() && info.Size() > 0
}
