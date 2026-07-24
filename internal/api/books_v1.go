package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/librarr/internal/library"
)

const normalizedReadAPIError = "Librarr v1 book API requires LIBRARR_LIBRARY_REPOSITORY_MODE=normalized"

type v1BookListResponse struct {
	Items      []v1BookSummary   `json:"items"`
	Pagination v1PaginationBlock `json:"pagination"`
}

type v1PaginationBlock struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
	Total  int `json:"total"`
}

type v1SeriesSummary struct {
	Name     *string  `json:"name"`
	Position *float64 `json:"position"`
}

type v1ContributorSummary struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type v1PrimaryAuthorSummary struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type v1IdentifierSummary struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type v1CoverSummary struct {
	Available bool    `json:"available"`
	URL       *string `json:"url"`
}

type v1BookSummary struct {
	ID            int64                   `json:"id"`
	Title         string                  `json:"title"`
	SortTitle     string                  `json:"sort_title"`
	MediaType     string                  `json:"media_type"`
	Description   string                  `json:"description"`
	Series        v1SeriesSummary         `json:"series"`
	PrimaryAuthor *v1PrimaryAuthorSummary `json:"primary_author"`
	Contributors  []v1ContributorSummary  `json:"contributors"`
	Identifiers   []v1IdentifierSummary   `json:"identifiers"`
	Formats       []string                `json:"formats"`
	EditionCount  int                     `json:"edition_count"`
	FileCount     int                     `json:"file_count"`
	Cover         v1CoverSummary          `json:"cover"`
	CreatedAt     string                  `json:"created_at"`
	UpdatedAt     string                  `json:"updated_at"`
}

type v1BookDetailResponse struct {
	ID              int64                   `json:"id"`
	Title           string                  `json:"title"`
	SortTitle       string                  `json:"sort_title"`
	MediaType       string                  `json:"media_type"`
	Description     string                  `json:"description"`
	PublicationYear int                     `json:"publication_year,omitempty"`
	Language        string                  `json:"language,omitempty"`
	Series          v1SeriesSummary         `json:"series"`
	PrimaryAuthor   *v1PrimaryAuthorSummary `json:"primary_author"`
	Contributors    []v1ContributorSummary  `json:"contributors"`
	Identifiers     []v1IdentifierSummary   `json:"identifiers"`
	Formats         []string                `json:"formats"`
	FileCount       int                     `json:"file_count"`
	EditionCount    int                     `json:"edition_count"`
	Editions        []v1EditionSummary      `json:"editions"`
	Cover           v1CoverSummary          `json:"cover"`
	CreatedAt       string                  `json:"created_at"`
	UpdatedAt       string                  `json:"updated_at"`
}

type v1EditionSummary struct {
	ID              int64                  `json:"id"`
	Title           string                 `json:"title"`
	Subtitle        string                 `json:"subtitle,omitempty"`
	Publisher       string                 `json:"publisher,omitempty"`
	PublicationDate string                 `json:"publication_date,omitempty"`
	Language        string                 `json:"language,omitempty"`
	Contributors    []v1ContributorSummary `json:"contributors,omitempty"`
	Identifiers     []v1IdentifierSummary  `json:"identifiers"`
	Formats         []string               `json:"formats"`
	FileCount       int                    `json:"file_count"`
}

type v1FileSummary struct {
	ID           int64  `json:"id"`
	EditionID    int64  `json:"edition_id"`
	Path         string `json:"path,omitempty"`
	OriginalPath string `json:"original_path,omitempty"`
	Format       string `json:"format"`
	MediaType    string `json:"media_type"`
	Size         int64  `json:"size"`
	SourceID     string `json:"source_id,omitempty"`
	ContentHash  string `json:"content_hash,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

func (s *Server) handleV1Books(w http.ResponseWriter, r *http.Request) {
	if !s.ensureNormalizedReadAPI(w) {
		return
	}

	sortValue := strings.TrimSpace(r.URL.Query().Get("sort"))
	if sortValue == "" {
		sortValue = "recently_added"
	}
	if !slices.Contains([]string{"title", "author", "recently_added", "recently_updated"}, sortValue) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false,
			"error":   "Invalid sort value",
		})
		return
	}

	orderValue := strings.TrimSpace(r.URL.Query().Get("order"))
	if orderValue == "" {
		if sortValue == "title" || sortValue == "author" {
			orderValue = "asc"
		} else {
			orderValue = "desc"
		}
	}
	if !slices.Contains([]string{"asc", "desc"}, strings.ToLower(orderValue)) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false,
			"error":   "Invalid order value",
		})
		return
	}

	query := library.ListBooksQuery{
		MediaType: library.MediaType(strings.TrimSpace(r.URL.Query().Get("media_type"))),
		Search:    strings.TrimSpace(r.URL.Query().Get("search")),
		Format:    strings.TrimSpace(r.URL.Query().Get("format")),
		Sort:      sortValue,
		Order:     strings.ToLower(orderValue),
		Limit:     queryBoundedInt(r, "limit", 50, 1, 500),
		Offset:    queryBoundedInt(r, "offset", 0, 0, 1_000_000),
	}
	if query.MediaType != "" && !query.MediaType.Valid() {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false,
			"error":   "Invalid media_type value",
		})
		return
	}

	total, err := s.library().CountListedBooks(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to count books", err)
		return
	}

	books, err := s.library().ListBooks(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to load books", err)
		return
	}

	items := make([]v1BookSummary, 0, len(books))
	for _, book := range books {
		summary, err := s.buildV1BookSummary(r.Context(), book)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to build book response", err)
			return
		}
		items = append(items, summary)
	}

	writeJSON(w, http.StatusOK, v1BookListResponse{
		Items: items,
		Pagination: v1PaginationBlock{
			Limit:  query.Limit,
			Offset: query.Offset,
			Total:  total,
		},
	})
}

func (s *Server) handleV1Book(w http.ResponseWriter, r *http.Request) {
	if !s.ensureNormalizedReadAPI(w) {
		return
	}
	bookID, ok := parseIDPathValue(w, r, "id", "Invalid book ID")
	if !ok {
		return
	}

	book, err := s.library().GetBook(r.Context(), bookID)
	if errors.Is(err, library.ErrBookNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": "Book not found"})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to load book", err)
		return
	}

	detail, err := s.buildV1BookDetail(r.Context(), *book)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to build book response", err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleV1BookFiles(w http.ResponseWriter, r *http.Request) {
	if !s.ensureNormalizedReadAPI(w) {
		return
	}
	bookID, ok := parseIDPathValue(w, r, "id", "Invalid book ID")
	if !ok {
		return
	}
	if _, err := s.library().GetBook(r.Context(), bookID); errors.Is(err, library.ErrBookNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": "Book not found"})
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to load book", err)
		return
	}

	files, err := s.library().GetBookFiles(r.Context(), bookID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to load files", err)
		return
	}
	items := make([]v1FileSummary, 0, len(files))
	for _, file := range files {
		items = append(items, v1FileSummary{
			ID:           file.ID,
			EditionID:    file.EditionID,
			Path:         file.Path,
			OriginalPath: file.OriginalPath,
			Format:       file.Format,
			MediaType:    string(file.MediaType),
			Size:         file.Size,
			SourceID:     file.SourceID,
			ContentHash:  file.ContentHash,
			CreatedAt:    formatAPITime(file.CreatedAt),
			UpdatedAt:    formatAPITime(file.UpdatedAt),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleV1BookEditions(w http.ResponseWriter, r *http.Request) {
	if !s.ensureNormalizedReadAPI(w) {
		return
	}
	bookID, ok := parseIDPathValue(w, r, "id", "Invalid book ID")
	if !ok {
		return
	}
	if _, err := s.library().GetBook(r.Context(), bookID); errors.Is(err, library.ErrBookNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": "Book not found"})
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to load book", err)
		return
	}

	editions, err := s.buildV1Editions(r.Context(), bookID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to load editions", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": editions})
}

func (s *Server) ensureNormalizedReadAPI(w http.ResponseWriter) bool {
	mode, err := s.cfg.NormalizedLibraryRepositoryMode()
	if err != nil || mode != "normalized" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"success": false,
			"error":   normalizedReadAPIError,
		})
		return false
	}
	return true
}

func parseIDPathValue(w http.ResponseWriter, r *http.Request, key, message string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(key), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false,
			"error":   message,
		})
		return 0, false
	}
	return id, true
}

func (s *Server) buildV1BookSummary(ctx context.Context, book library.Book) (v1BookSummary, error) {
	detail, err := s.buildV1BookDetail(ctx, book)
	if err != nil {
		return v1BookSummary{}, err
	}
	return v1BookSummary{
		ID:            detail.ID,
		Title:         detail.Title,
		SortTitle:     detail.SortTitle,
		MediaType:     detail.MediaType,
		Description:   detail.Description,
		Series:        detail.Series,
		PrimaryAuthor: detail.PrimaryAuthor,
		Contributors:  detail.Contributors,
		Identifiers:   detail.Identifiers,
		Formats:       detail.Formats,
		EditionCount:  detail.EditionCount,
		FileCount:     detail.FileCount,
		Cover:         detail.Cover,
		CreatedAt:     detail.CreatedAt,
		UpdatedAt:     detail.UpdatedAt,
	}, nil
}

func (s *Server) buildV1BookDetail(ctx context.Context, book library.Book) (v1BookDetailResponse, error) {
	editions, err := s.buildV1Editions(ctx, book.ID)
	if err != nil {
		return v1BookDetailResponse{}, err
	}
	files, err := s.library().GetBookFiles(ctx, book.ID)
	if err != nil {
		return v1BookDetailResponse{}, err
	}
	contributors, primaryAuthor := aggregateContributors(editions)
	formats := uniqueFormats(files)
	return v1BookDetailResponse{
		ID:              book.ID,
		Title:           book.Title,
		SortTitle:       book.SortTitle,
		MediaType:       string(book.MediaType),
		Description:     book.Description,
		PublicationYear: book.PublicationYear,
		Language:        book.Language,
		Series:          mapSeries(book.Series),
		PrimaryAuthor:   primaryAuthor,
		Contributors:    contributors,
		Identifiers:     mapIdentifiers(book.Identifiers),
		Formats:         formats,
		FileCount:       len(files),
		EditionCount:    len(editions),
		Editions:        editions,
		Cover:           mapCover(book.Covers),
		CreatedAt:       formatAPITime(book.CreatedAt),
		UpdatedAt:       formatAPITime(book.UpdatedAt),
	}, nil
}

func (s *Server) buildV1Editions(ctx context.Context, bookID int64) ([]v1EditionSummary, error) {
	editions, err := s.library().ListBookEditions(ctx, bookID)
	if err != nil {
		return nil, err
	}
	allFiles, err := s.library().GetBookFiles(ctx, bookID)
	if err != nil {
		return nil, err
	}
	summaries := make([]v1EditionSummary, 0, len(editions))
	for _, edition := range editions {
		full, err := s.library().GetEdition(ctx, edition.ID)
		if err != nil {
			return nil, err
		}
		var editionFiles []library.BookFile
		for _, file := range allFiles {
			if file.EditionID == edition.ID {
				editionFiles = append(editionFiles, file)
			}
		}
		summaries = append(summaries, v1EditionSummary{
			ID:              full.ID,
			Title:           full.Title,
			Subtitle:        full.Subtitle,
			Publisher:       full.Publisher,
			PublicationDate: full.PublicationDate,
			Language:        full.Language,
			Contributors:    mapContributors(full.Contributors),
			Identifiers:     mapIdentifiers(full.Identifiers),
			Formats:         uniqueFormats(editionFiles),
			FileCount:       len(editionFiles),
		})
	}
	return summaries, nil
}

func mapSeries(series []library.BookSeries) v1SeriesSummary {
	if len(series) == 0 {
		return v1SeriesSummary{}
	}
	name := series[0].Series.Title
	var position *float64
	if series[0].Position != 0 {
		pos := series[0].Position
		position = &pos
	}
	return v1SeriesSummary{
		Name:     &name,
		Position: position,
	}
}

func mapContributors(contributors []library.Contributor) []v1ContributorSummary {
	items := make([]v1ContributorSummary, 0, len(contributors))
	for _, contributor := range contributors {
		role := ""
		if len(contributor.Roles) > 0 {
			role = string(contributor.Roles[0])
		}
		items = append(items, v1ContributorSummary{
			ID:   contributor.ID,
			Name: contributor.Name,
			Role: role,
		})
	}
	return items
}

func mapIdentifiers(identifiers []library.Identifier) []v1IdentifierSummary {
	items := make([]v1IdentifierSummary, 0, len(identifiers))
	for _, identifier := range identifiers {
		items = append(items, v1IdentifierSummary{
			Type:  identifier.Provider,
			Value: identifier.Value,
		})
	}
	return items
}

func mapCover(covers []library.Cover) v1CoverSummary {
	if len(covers) == 0 {
		return v1CoverSummary{}
	}
	for _, cover := range covers {
		if strings.TrimSpace(cover.SourceURL) != "" {
			url := cover.SourceURL
			return v1CoverSummary{Available: true, URL: &url}
		}
		if strings.TrimSpace(cover.LocalPath) != "" {
			return v1CoverSummary{Available: true}
		}
	}
	return v1CoverSummary{Available: true}
}

func aggregateContributors(editions []v1EditionSummary) ([]v1ContributorSummary, *v1PrimaryAuthorSummary) {
	seen := make(map[string]struct{})
	contributors := make([]v1ContributorSummary, 0)
	var primary *v1PrimaryAuthorSummary
	for _, edition := range editions {
		for _, contributor := range edition.Contributors {
			key := fmt.Sprintf("%d:%s:%s", contributor.ID, contributor.Name, contributor.Role)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			contributors = append(contributors, contributor)
			if primary == nil && strings.EqualFold(contributor.Role, string(library.RoleAuthor)) {
				primary = &v1PrimaryAuthorSummary{ID: contributor.ID, Name: contributor.Name}
			}
		}
	}
	if primary == nil && len(contributors) > 0 {
		primary = &v1PrimaryAuthorSummary{ID: contributors[0].ID, Name: contributors[0].Name}
	}
	return contributors, primary
}

func uniqueFormats(files []library.BookFile) []string {
	seen := map[string]struct{}{}
	formats := make([]string, 0, len(files))
	for _, file := range files {
		format := strings.TrimSpace(strings.ToLower(file.Format))
		if format == "" {
			continue
		}
		if _, ok := seen[format]; ok {
			continue
		}
		seen[format] = struct{}{}
		formats = append(formats, format)
	}
	slices.Sort(formats)
	return formats
}

func formatAPITime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
