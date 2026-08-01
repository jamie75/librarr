package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jamie75/librarr/internal/models"
	"github.com/jamie75/librarr/internal/safepath"
)

type appleBooksExportResponse struct {
	ID              int64  `json:"id"`
	BookID          int64  `json:"book_id"`
	MediaType       string `json:"media_type"`
	BookTitle       string `json:"book_title,omitempty"`
	RequestedFormat string `json:"requested_format"`
	ActualFormat    string `json:"actual_format"`
	Status          string `json:"status"`
	SourceFileCount int    `json:"source_file_count"`
	SourceBytes     int64  `json:"source_bytes"`
	Destination     string `json:"destination"`
	Checksum        string `json:"checksum,omitempty"`
	Error           string `json:"error,omitempty"`
	CreatedAt       string `json:"created_at"`
	CompletedAt     string `json:"completed_at,omitempty"`
	UpdatedAt       string `json:"updated_at"`
}

func (s *Server) handleAppleBooksExport(w http.ResponseWriter, r *http.Request) {
	if !s.ensureNormalizedReadAPI(w) {
		return
	}
	bookID, ok := parseIDPathValue(w, r, "id", "Invalid book ID")
	if !ok {
		return
	}
	var request struct {
		Format string `json:"format"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&request)
	}
	if s.appleBooks == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "error": "Apple Books export is unavailable"})
		return
	}
	export, err := s.appleBooks.Export(r.Context(), bookID, request.Format)
	if err != nil {
		status := http.StatusInternalServerError
		message := sanitizeAppleBooksError(err)
		if strings.Contains(message, "disabled") || strings.Contains(message, "unavailable") {
			status = http.StatusServiceUnavailable
		} else if strings.Contains(message, "only audiobooks") || strings.Contains(message, "only audiobooks and ebooks") || strings.Contains(message, "requested format") || strings.Contains(message, "not supported for ebooks") || strings.Contains(message, "not available for this ebook") {
			status = http.StatusUnprocessableEntity
		} else if strings.Contains(message, "already exists") {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]any{"success": false, "error": message})
		return
	}
	response, err := s.appleBooksResponse(r.Context(), export)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to build Apple Books export response", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "export": response})
}

func (s *Server) handleAppleBooksBookExports(w http.ResponseWriter, r *http.Request) {
	if !s.ensureNormalizedReadAPI(w) {
		return
	}
	bookID, ok := parseIDPathValue(w, r, "id", "Invalid book ID")
	if !ok {
		return
	}
	s.writeAppleBooksExports(w, r, bookID)
}

func (s *Server) handleAppleBooksExports(w http.ResponseWriter, r *http.Request) {
	if !s.ensureNormalizedReadAPI(w) {
		return
	}
	s.writeAppleBooksExports(w, r, 0)
}

func (s *Server) writeAppleBooksExports(w http.ResponseWriter, r *http.Request, bookID int64) {
	if s.appleBooks == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "error": "Apple Books export is unavailable"})
		return
	}
	exports, err := s.db.ListAppleBooksExports(bookID, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to load Apple Books exports", err)
		return
	}
	items := make([]appleBooksExportResponse, 0, len(exports))
	for i := range exports {
		item, err := s.appleBooksResponse(r.Context(), &exports[i])
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to build Apple Books export response", err)
			return
		}
		items = append(items, *item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) handleAppleBooksExportByID(w http.ResponseWriter, r *http.Request) {
	if !s.ensureNormalizedReadAPI(w) {
		return
	}
	exportID, err := strconv.ParseInt(r.PathValue("export_id"), 10, 64)
	if err != nil || exportID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Invalid export ID"})
		return
	}
	export, err := s.db.GetAppleBooksExport(exportID)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": "Export not found"})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to load Apple Books export", err)
		return
	}
	response, err := s.appleBooksResponse(r.Context(), export)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to build Apple Books export response", err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleAppleBooksTest(w http.ResponseWriter, _ *http.Request) {
	if s.appleBooks == nil || !s.cfg.AppleBooksExportEnabled {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Apple Books export is disabled"})
		return
	}
	root := strings.TrimSpace(s.cfg.AppleBooksExportDir)
	if !filepath.IsAbs(root) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Apple Books export folder must be absolute"})
		return
	}
	cleanedRoot := filepath.Clean(root)
	validatedRoot, err := safepath.UnderRoot(filepath.Dir(cleanedRoot), cleanedRoot)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Apple Books export folder is outside the configured root"})
		return
	}
	if err := os.MkdirAll(validatedRoot, 0755); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Apple Books export folder is not writable"})
		return
	}
	validatedRoot, err = safepath.ExistingUnderRoot(validatedRoot, validatedRoot)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Apple Books export folder is outside the configured root"})
		return
	}
	probe, err := os.CreateTemp(validatedRoot, ".librarr-apple-books-probe-")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Apple Books export folder is not writable"})
		return
	}
	name := probe.Name()
	closeErr := probe.Close()
	validatedProbe, validateErr := safepath.ExistingUnderRoot(validatedRoot, name)
	removeErr := validateErr
	if validateErr == nil {
		removeErr = os.Remove(validatedProbe)
	}
	if closeErr != nil || removeErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Apple Books export folder is not writable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Apple Books export folder is writable"})
}

func (s *Server) appleBooksResponse(ctx context.Context, export *models.AppleBooksExport) (*appleBooksExportResponse, error) {
	if export == nil {
		return nil, errors.New("export is nil")
	}
	destination := export.DestinationName
	if root := strings.TrimSpace(s.cfg.AppleBooksExportDir); root != "" && export.DestinationPath != "" {
		if relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(export.DestinationPath)); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			destination = filepath.ToSlash(relative)
		}
	}
	bookTitle := ""
	if book, err := s.library().GetBook(ctx, export.BookID); err == nil {
		bookTitle = book.Title
	}
	return &appleBooksExportResponse{
		ID: export.ID, BookID: export.BookID, BookTitle: bookTitle, MediaType: export.MediaType,
		RequestedFormat: export.RequestedFormat, ActualFormat: export.ActualFormat,
		Status: export.Status, SourceFileCount: export.SourceFileCount,
		SourceBytes: export.SourceBytes, Destination: destination,
		Checksum: export.Checksum, Error: sanitizeAppleBooksErrorString(export.Error),
		CreatedAt: formatAPITime(export.CreatedAt), CompletedAt: formatOptionalTime(export.CompletedAt),
		UpdatedAt: formatAPITime(export.UpdatedAt),
	}, nil
}

func sanitizeAppleBooksError(err error) string {
	if err == nil {
		return ""
	}
	return sanitizeAppleBooksErrorString(err.Error())
}

func sanitizeAppleBooksErrorString(value string) string {
	value = strings.NewReplacer("\r", " ", "\n", " ", "\x00", " ").Replace(strings.TrimSpace(value))
	if len(value) > 500 {
		return value[:500]
	}
	return value
}

func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return formatAPITime(*value)
}
