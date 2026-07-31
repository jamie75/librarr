package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRefreshBookMetadataConvertsLegacyAudiobookDirectory(t *testing.T) {
	repo, cleanup := newNormalizedRepo(t)
	defer cleanup()
	service, err := NewLibraryService(ServiceOptions{
		BookRepository: repo, EditionRepository: repo, FileRepository: repo,
		MetadataRepository: repo, SeriesRepository: repo, ContributorRepository: repo,
		IdentifierRepository: repo, CoverRepository: repo, TransactionManager: repo,
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range []string{"Jane Doe - The First Chapter.mp3", "Jane Doe - The First Chapter 02.mp3"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("not audio but a catalogable track"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	book, err := service.CreateBook(ctx, Book{Title: "Old Directory", MediaType: MediaTypeAudiobook, Status: BookStatusOwned})
	if err != nil {
		t.Fatal(err)
	}
	edition, err := service.CreateEdition(ctx, Edition{BookID: book.ID, Title: "Old Directory"})
	if err != nil {
		t.Fatal(err)
	}
	file, err := repo.AttachFile(ctx, BookFile{EditionID: edition.ID, BookID: book.ID, MediaType: MediaTypeAudiobook, Format: "directory", Path: dir})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.RefreshBookMetadata(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Refreshed || !result.Updated {
		t.Fatalf("result = %+v", result)
	}
	if !containsString(result.ChangedFields, "format") || !containsString(result.ChangedFields, "track_count") {
		t.Fatalf("changed fields = %+v", result.ChangedFields)
	}
	refreshed, err := service.GetFile(ctx, file.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Format != "mp3" {
		t.Fatalf("format = %q, want mp3", refreshed.Format)
	}
	if refreshed.EmbeddedMetadata["track_count"] != "2" {
		t.Fatalf("metadata = %+v", refreshed.EmbeddedMetadata)
	}
	if refreshed.EmbeddedMetadata["metadata_source"] != "filename_parsing" {
		t.Fatalf("metadata source = %+v", refreshed.EmbeddedMetadata)
	}
	contributors, err := service.GetEditionContributors(ctx, edition.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(contributors) != 1 || contributors[0].Name != "Jane Doe" {
		t.Fatalf("contributors = %+v", contributors)
	}

	second, err := service.RefreshBookMetadata(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Refreshed || second.Updated {
		t.Fatalf("second refresh should be idempotent: %+v", second)
	}
	books, err := service.ListBooks(ctx, ListBooksQuery{Limit: 100})
	if err != nil || len(books) != 1 {
		t.Fatalf("books = %+v, err = %v", books, err)
	}
	files, err := service.GetBookFiles(ctx, book.ID)
	if err != nil || len(files) != 2 || files[0].ID != file.ID {
		t.Fatalf("files = %+v, err = %v", files, err)
	}
}

func TestRefreshBookMetadataSkipsStoredPathOutsideConfiguredRoots(t *testing.T) {
	repo, cleanup := newNormalizedRepo(t)
	defer cleanup()
	service, err := NewLibraryService(ServiceOptions{
		BookRepository: repo, EditionRepository: repo, FileRepository: repo,
		MetadataRepository: repo, SeriesRepository: repo, ContributorRepository: repo,
		IdentifierRepository: repo, CoverRepository: repo, TransactionManager: repo,
	})
	if err != nil {
		t.Fatal(err)
	}
	allowedRoot := t.TempDir()
	outsideRoot := t.TempDir()
	outsidePath := filepath.Join(outsideRoot, "outside.mp3")
	if err := os.WriteFile(outsidePath, []byte("not audio"), 0600); err != nil {
		t.Fatal(err)
	}
	service.SetAllowedFileRoots(allowedRoot)
	ctx := context.Background()
	book, err := service.CreateBook(ctx, Book{Title: "Outside", MediaType: MediaTypeAudiobook})
	if err != nil {
		t.Fatal(err)
	}
	edition, err := service.CreateEdition(ctx, Edition{BookID: book.ID, Title: "Outside"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AttachFile(ctx, BookFile{EditionID: edition.ID, BookID: book.ID, MediaType: MediaTypeAudiobook, Format: "mp3", Path: outsidePath}); err != nil {
		t.Fatal(err)
	}
	result, err := service.RefreshBookMetadata(ctx, book.ID)
	if err == nil || result.Refreshed || result.Updated {
		t.Fatalf("unsafe refresh result = %+v, err = %v", result, err)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestRefreshBookMetadataPreservesManualTitleAndReportsConflict(t *testing.T) {
	repo, cleanup := newNormalizedRepo(t)
	defer cleanup()
	service, err := NewLibraryService(ServiceOptions{
		BookRepository: repo, EditionRepository: repo, FileRepository: repo,
		MetadataRepository: repo, SeriesRepository: repo, ContributorRepository: repo,
		IdentifierRepository: repo, CoverRepository: repo, TransactionManager: repo,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "Author - Embedded Title.mp3")
	if err := os.WriteFile(path, []byte("not audio"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	book, err := service.CreateBook(ctx, Book{Title: "Manual Title", MediaType: MediaTypeAudiobook})
	if err != nil {
		t.Fatal(err)
	}
	edition, err := service.CreateEdition(ctx, Edition{BookID: book.ID, Title: "Manual Title"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AttachFile(ctx, BookFile{EditionID: edition.ID, MediaType: MediaTypeAudiobook, Format: "directory", Path: path}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PatchBookMetadata(ctx, book.ID, BookMetadataPatch{Fields: map[MetadataField]string{MetadataFieldTitle: "Manual Title"}}); err != nil {
		t.Fatal(err)
	}
	result, err := service.RefreshBookMetadata(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ManualReview || len(result.Conflicts) != 1 {
		t.Fatalf("result = %+v", result)
	}
	updated, err := service.GetBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Manual Title" {
		t.Fatalf("title = %q", updated.Title)
	}
}
