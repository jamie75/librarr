package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/JeremiahM37/librarr/internal/config"
	"github.com/JeremiahM37/librarr/internal/db"
	"github.com/JeremiahM37/librarr/internal/library"
)

func TestV1BooksListSupportsPaginationSortSearchAndFormat(t *testing.T) {
	s, ids, cleanup := newNormalizedBooksAPIServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/books?media_type=ebook&sort=author&order=asc&limit=1&offset=1", nil)
	rr := httptest.NewRecorder()
	s.handleV1Books(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var body struct {
		Items      []v1BookSummary `json:"items"`
		Pagination struct {
			Limit  int `json:"limit"`
			Offset int `json:"offset"`
			Total  int `json:"total"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Pagination.Total != 2 || body.Pagination.Limit != 1 || body.Pagination.Offset != 1 {
		t.Fatalf("pagination = %+v", body.Pagination)
	}
	if len(body.Items) != 1 || body.Items[0].ID != ids.darkMatterID {
		t.Fatalf("items = %+v", body.Items)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/books?media_type=ebook&search=andy&format=mobi", nil)
	rr = httptest.NewRecorder()
	s.handleV1Books(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].ID != ids.projectHailMaryID {
		t.Fatalf("filtered items = %+v", body.Items)
	}
	if body.Items[0].PrimaryAuthor == nil || body.Items[0].PrimaryAuthor.Name != "Andy Weir" {
		t.Fatalf("primary author = %+v", body.Items[0].PrimaryAuthor)
	}
}

func TestV1BookDetailFilesAndEditions(t *testing.T) {
	s, ids, cleanup := newNormalizedBooksAPIServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/books/%d", ids.projectHailMaryID), nil)
	req.SetPathValue("id", fmt.Sprint(ids.projectHailMaryID))
	rr := httptest.NewRecorder()
	s.handleV1Book(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var detail v1BookDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Title != "Project Hail Mary" {
		t.Fatalf("detail = %+v", detail)
	}
	if detail.Series.Name == nil || *detail.Series.Name != "The Ryland Grace Files" {
		t.Fatalf("series = %+v", detail.Series)
	}
	if detail.Cover.URL == nil || *detail.Cover.URL != "https://covers.example/project-hail-mary.jpg" {
		t.Fatalf("cover = %+v", detail.Cover)
	}
	if len(detail.Editions) != 1 || detail.Editions[0].FileCount != 2 {
		t.Fatalf("editions = %+v", detail.Editions)
	}
	if len(detail.Formats) != 2 || detail.FileCount != 2 {
		t.Fatalf("formats=%v file_count=%d", detail.Formats, detail.FileCount)
	}

	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/books/%d/files", ids.projectHailMaryID), nil)
	req.SetPathValue("id", fmt.Sprint(ids.projectHailMaryID))
	rr = httptest.NewRecorder()
	s.handleV1BookFiles(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("files status = %d body=%s", rr.Code, rr.Body.String())
	}
	var files struct {
		Items []v1FileSummary `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &files); err != nil {
		t.Fatal(err)
	}
	if len(files.Items) != 2 {
		t.Fatalf("files = %+v", files.Items)
	}
	if files.Items[0].ContentHash == "" {
		t.Fatalf("expected content hash to be exposed consistently with existing project behavior")
	}

	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/books/%d/editions", ids.projectHailMaryID), nil)
	req.SetPathValue("id", fmt.Sprint(ids.projectHailMaryID))
	rr = httptest.NewRecorder()
	s.handleV1BookEditions(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("editions status = %d body=%s", rr.Code, rr.Body.String())
	}
	var editions struct {
		Items []v1EditionSummary `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &editions); err != nil {
		t.Fatal(err)
	}
	if len(editions.Items) != 1 || editions.Items[0].Title != "Project Hail Mary" {
		t.Fatalf("edition items = %+v", editions.Items)
	}
}

func TestV1BooksRejectInvalidParamsAndMissingBooks(t *testing.T) {
	s, _, cleanup := newNormalizedBooksAPIServer(t)
	defer cleanup()

	for _, path := range []string{
		"/api/v1/books?media_type=invalid",
		"/api/v1/books?sort=bad",
		"/api/v1/books?order=sideways",
		"/api/v1/books/not-a-number",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		switch {
		case path == "/api/v1/books/not-a-number":
			req.SetPathValue("id", "not-a-number")
			s.handleV1Book(rr, req)
		default:
			s.handleV1Books(rr, req)
		}
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d body=%s", path, rr.Code, rr.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/books/9999", nil)
	req.SetPathValue("id", "9999")
	rr := httptest.NewRecorder()
	s.handleV1Book(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestV1BooksRejectLegacyModeAndCompatibilityEndpointRemainsAvailable(t *testing.T) {
	d, err := db.New(filepath.Join(t.TempDir(), "legacy-books.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if _, err := d.SQLDB().Exec(`INSERT INTO library_items (title, author, file_path, file_format, media_type, source_id)
		VALUES ('Legacy Book', 'Legacy Author', '/books/legacy.epub', 'epub', 'ebook', 'legacy-1')`); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{LibraryRepositoryMode: "legacy"}
	selection, err := library.NewConfiguredLibraryService(context.Background(), cfg, d)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: cfg, db: d, libraryService: selection.LibraryService}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/books", nil)
	rr := httptest.NewRecorder()
	s.handleV1Books(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var errorBody map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &errorBody); err != nil {
		t.Fatal(err)
	}
	if errorBody["error"] != normalizedReadAPIError {
		t.Fatalf("error body = %+v", errorBody)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/library", nil)
	rr = httptest.NewRecorder()
	s.handleLibrary(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("compat status = %d body=%s", rr.Code, rr.Body.String())
	}
	var compat struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &compat); err != nil {
		t.Fatal(err)
	}
	if compat.Total != 1 || len(compat.Items) != 1 || compat.Items[0]["title"] != "Legacy Book" {
		t.Fatalf("compat = %+v", compat)
	}
}

type normalizedBookAPIIDs struct {
	projectHailMaryID int64
	darkMatterID      int64
}

func newNormalizedBooksAPIServer(t *testing.T) (*Server, normalizedBookAPIIDs, func()) {
	t.Helper()
	d, err := db.New(filepath.Join(t.TempDir(), "books-v1.db"))
	if err != nil {
		t.Fatal(err)
	}
	seedCompletedEmptyBackfill(t, d)

	cfg := &config.Config{LibraryRepositoryMode: "normalized"}
	selection, err := library.NewConfiguredLibraryService(context.Background(), cfg, d)
	if err != nil {
		t.Fatal(err)
	}
	repo, ok := selection.Repository.(*library.NormalizedRepository)
	if !ok {
		t.Fatalf("repository type = %T", selection.Repository)
	}

	ctx := context.Background()
	firstBook, err := selection.LibraryService.CreateBook(ctx, library.Book{
		Title:     "Project Hail Mary",
		SortTitle: "Project Hail Mary",
		MediaType: library.MediaTypeEbook,
		Status:    library.BookStatusOwned,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstEdition, err := selection.LibraryService.CreateEdition(ctx, library.Edition{
		BookID: firstBook.ID,
		Title:  "Project Hail Mary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := selection.LibraryService.AttachContributor(ctx, firstEdition.ID, library.Contributor{Name: "Andy Weir", Roles: []library.ContributorRole{library.RoleAuthor}}); err != nil {
		t.Fatal(err)
	}
	if _, err := selection.LibraryService.AddIdentifier(ctx, library.Identifier{
		Scope:    library.IdentifierScopeBook,
		Provider: "isbn13",
		Value:    "9780593135204",
		Source:   fmt.Sprint(firstBook.ID),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AttachBook(ctx, firstBook.ID, library.BookSeries{
		Series:          library.Series{Title: "The Ryland Grace Files"},
		Position:        1,
		DisplayPosition: "1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AttachCover(ctx, library.Cover{
		BookID:    firstBook.ID,
		Source:    "test",
		SourceURL: "https://covers.example/project-hail-mary.jpg",
		IsPrimary: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := selection.LibraryService.AttachFile(ctx, library.BookFile{
		EditionID:    firstEdition.ID,
		MediaType:    library.MediaTypeEbook,
		Format:       "epub",
		Path:         "/books/project-hail-mary.epub",
		OriginalPath: "/incoming/project-hail-mary.epub",
		Size:         100,
		ContentHash:  "hash-epub",
		SourceID:     "source-epub",
		SourceType:   "test",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := selection.LibraryService.AttachFile(ctx, library.BookFile{
		EditionID:    firstEdition.ID,
		MediaType:    library.MediaTypeEbook,
		Format:       "mobi",
		Path:         "/books/project-hail-mary.mobi",
		OriginalPath: "/incoming/project-hail-mary.mobi",
		Size:         120,
		ContentHash:  "hash-mobi",
		SourceID:     "source-mobi",
		SourceType:   "test",
	}); err != nil {
		t.Fatal(err)
	}

	secondBook, err := selection.LibraryService.CreateBook(ctx, library.Book{
		Title:     "Dark Matter",
		SortTitle: "Dark Matter",
		MediaType: library.MediaTypeEbook,
		Status:    library.BookStatusOwned,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondEdition, err := selection.LibraryService.CreateEdition(ctx, library.Edition{
		BookID: secondBook.ID,
		Title:  "Dark Matter",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := selection.LibraryService.AttachContributor(ctx, secondEdition.ID, library.Contributor{Name: "Blake Crouch", Roles: []library.ContributorRole{library.RoleAuthor}}); err != nil {
		t.Fatal(err)
	}
	if _, err := selection.LibraryService.AttachFile(ctx, library.BookFile{
		EditionID:    secondEdition.ID,
		MediaType:    library.MediaTypeEbook,
		Format:       "epub",
		Path:         "/books/dark-matter.epub",
		OriginalPath: "/incoming/dark-matter.epub",
		Size:         90,
		ContentHash:  "hash-dark-matter",
		SourceID:     "source-dark-matter",
		SourceType:   "test",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := d.SQLDB().Exec(`UPDATE books SET created_at = '2026-07-20 10:00:00', updated_at = '2026-07-20 10:00:00' WHERE id = ?`, firstBook.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQLDB().Exec(`UPDATE books SET created_at = '2026-07-21 10:00:00', updated_at = '2026-07-21 10:00:00' WHERE id = ?`, secondBook.ID); err != nil {
		t.Fatal(err)
	}

	return &Server{cfg: cfg, db: d, libraryService: selection.LibraryService}, normalizedBookAPIIDs{
		projectHailMaryID: firstBook.ID,
		darkMatterID:      secondBook.ID,
	}, func() { _ = d.Close() }
}
