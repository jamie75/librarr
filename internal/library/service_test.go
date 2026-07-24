package library

import (
	"context"
	"errors"
	"testing"

	"github.com/JeremiahM37/librarr/internal/models"
)

func TestNewLibraryServiceRequiresRepositories(t *testing.T) {
	if _, err := NewLibraryService(ServiceOptions{}); err == nil {
		t.Fatal("expected missing repository error")
	}

	repo := &serviceTestRepo{}
	svc, err := NewLibraryService(ServiceOptions{
		BookRepository:        repo,
		FileRepository:        repo,
		SeriesRepository:      repo,
		ContributorRepository: repo,
		IdentifierRepository:  repo,
		CoverRepository:       repo,
	})
	if err != nil {
		t.Fatalf("NewLibraryService failed: %v", err)
	}
	if svc == nil {
		t.Fatal("expected service")
	}
}

func TestLibraryServiceDelegatesToRepositories(t *testing.T) {
	repo := &serviceTestRepo{
		book: Book{ID: 42, Title: "Dune", MediaType: MediaTypeEbook},
		files: []BookFile{{
			ID:     7,
			BookID: 42,
			Path:   "/books/dune.epub",
			Format: "epub",
		}},
	}
	svc := mustService(t, repo)
	ctx := context.Background()

	book, err := svc.FindBook(ctx, BookQuery{Title: "Dune"})
	if err != nil {
		t.Fatal(err)
	}
	if book.ID != 42 || repo.findBookCalls != 1 {
		t.Fatalf("FindBook book=%+v calls=%d", book, repo.findBookCalls)
	}

	files, err := svc.GetBookFiles(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].ID != 7 || repo.getFilesBookID != 42 {
		t.Fatalf("files=%+v getFilesBookID=%d", files, repo.getFilesBookID)
	}
}

func TestLibraryServiceTranslatesDomainErrors(t *testing.T) {
	repo := &serviceTestRepo{err: ErrNotFound}
	svc := mustService(t, repo)

	if _, err := svc.FindBookByID(context.Background(), 999); !errors.Is(err, ErrBookNotFound) {
		t.Fatalf("FindBookByID error = %v", err)
	}

	repo.err = ErrReadOnlyRepository
	if err := svc.DeleteBook(context.Background(), 1); !errors.Is(err, ErrRepositoryReadOnly) {
		t.Fatalf("DeleteBook error = %v", err)
	}
}

func TestLibraryServiceRejectsInvalidIdentifier(t *testing.T) {
	svc := mustService(t, &serviceTestRepo{})
	_, err := svc.FindBookByIdentifier(context.Background(), Identifier{Provider: "isbn"})
	if !errors.Is(err, ErrInvalidIdentifier) {
		t.Fatalf("FindBookByIdentifier error = %v", err)
	}
}

func TestLibraryServiceUsesLegacyCompatibilityRepository(t *testing.T) {
	legacy := NewLegacyLibraryRepositoryWithStore(fakeLegacyStore{items: []models.LibraryItem{legacyTestItem()}})
	svc, err := NewLibraryService(ServiceOptions{
		BookRepository:        legacy,
		FileRepository:        legacy,
		SeriesRepository:      legacy,
		ContributorRepository: legacy,
		IdentifierRepository:  legacy,
		CoverRepository:       legacy,
		LegacyCompatibility:   legacy,
	})
	if err != nil {
		t.Fatal(err)
	}

	items, err := svc.ListLegacyItems(context.Background(), "ebook", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Title != "The Guardian's Path" {
		t.Fatalf("legacy items = %+v", items)
	}

	count, err := svc.CountLegacyItems(context.Background(), "ebook")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count = %d", count)
	}
}

func mustService(t *testing.T, repo *serviceTestRepo) *LibraryService {
	t.Helper()
	svc, err := NewLibraryService(ServiceOptions{
		BookRepository:        repo,
		FileRepository:        repo,
		SeriesRepository:      repo,
		ContributorRepository: repo,
		IdentifierRepository:  repo,
		CoverRepository:       repo,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

type serviceTestRepo struct {
	book           Book
	files          []BookFile
	err            error
	findBookCalls  int
	getFilesBookID int64
}

func (r *serviceTestRepo) CreateBook(context.Context, Book) (*Book, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &r.book, nil
}

func (r *serviceTestRepo) UpdateBook(context.Context, Book) (*Book, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &r.book, nil
}

func (r *serviceTestRepo) FindBook(context.Context, BookQuery) (*Book, error) {
	r.findBookCalls++
	if r.err != nil {
		return nil, r.err
	}
	return &r.book, nil
}

func (r *serviceTestRepo) FindBookByIdentifier(context.Context, Identifier) (*Book, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &r.book, nil
}

func (r *serviceTestRepo) GetBook(context.Context, int64) (*Book, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &r.book, nil
}

func (r *serviceTestRepo) ListBooks(context.Context, ListBooksQuery) ([]Book, error) {
	if r.err != nil {
		return nil, r.err
	}
	return []Book{r.book}, nil
}

func (r *serviceTestRepo) SearchBooks(context.Context, BookQuery) ([]Book, error) {
	if r.err != nil {
		return nil, r.err
	}
	return []Book{r.book}, nil
}

func (r *serviceTestRepo) CountBooks(context.Context, BookQuery) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	return 1, nil
}

func (r *serviceTestRepo) RecentBooks(context.Context, ListBooksQuery) ([]Book, error) {
	if r.err != nil {
		return nil, r.err
	}
	return []Book{r.book}, nil
}

func (r *serviceTestRepo) SaveBook(context.Context, Book) (*Book, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &r.book, nil
}

func (r *serviceTestRepo) DeleteBook(context.Context, int64) error {
	return r.err
}

func (r *serviceTestRepo) GetBookFiles(_ context.Context, bookID int64) ([]BookFile, error) {
	r.getFilesBookID = bookID
	if r.err != nil {
		return nil, r.err
	}
	return r.files, nil
}

func (r *serviceTestRepo) GetFile(context.Context, int64) (*BookFile, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &r.files[0], nil
}

func (r *serviceTestRepo) FindFileByPath(context.Context, string) (*BookFile, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &r.files[0], nil
}

func (r *serviceTestRepo) FindByPath(ctx context.Context, path string) (*BookFile, error) {
	return r.FindFileByPath(ctx, path)
}

func (r *serviceTestRepo) FindFileBySourceID(context.Context, string) (*BookFile, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &r.files[0], nil
}

func (r *serviceTestRepo) FindFilesByContentHash(context.Context, string) ([]BookFile, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.files, nil
}

func (r *serviceTestRepo) ListFiles(context.Context, int64) ([]BookFile, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.files, nil
}

func (r *serviceTestRepo) AttachFile(context.Context, BookFile) (*BookFile, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &r.files[0], nil
}

func (r *serviceTestRepo) DetachFile(context.Context, int64) error {
	return r.err
}

func (r *serviceTestRepo) MoveFile(context.Context, int64, string) (*BookFile, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &r.files[0], nil
}

func (r *serviceTestRepo) DeleteFile(context.Context, int64) error {
	return r.err
}

func (r *serviceTestRepo) ValidateManagedFile(context.Context, int64) error {
	return r.err
}

func (r *serviceTestRepo) GetSeries(context.Context, string) (*Series, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &Series{Title: "Series"}, nil
}

func (r *serviceTestRepo) FindSeries(context.Context, string) ([]Series, error) {
	if r.err != nil {
		return nil, r.err
	}
	return []Series{{Title: "Series"}}, nil
}

func (r *serviceTestRepo) AttachBookToSeries(context.Context, int64, BookSeries) error {
	return r.err
}

func (r *serviceTestRepo) AttachBook(context.Context, int64, BookSeries) error {
	return r.err
}

func (r *serviceTestRepo) DetachBook(context.Context, int64, int64) error {
	return r.err
}

func (r *serviceTestRepo) SeriesPosition(context.Context, int64, int64) (BookSeries, error) {
	if r.err != nil {
		return BookSeries{}, r.err
	}
	return BookSeries{Series: Series{Title: "Series"}}, nil
}

func (r *serviceTestRepo) ListSeriesBooks(context.Context, int64) ([]Book, error) {
	if r.err != nil {
		return nil, r.err
	}
	return []Book{r.book}, nil
}

func (r *serviceTestRepo) MergeContributor(context.Context, Contributor) (*Contributor, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &Contributor{Name: "Author"}, nil
}

func (r *serviceTestRepo) GetEditionContributors(context.Context, int64) ([]Contributor, error) {
	if r.err != nil {
		return nil, r.err
	}
	return []Contributor{{Name: "Author"}}, nil
}

func (r *serviceTestRepo) AttachContributor(context.Context, int64, Contributor) error {
	return r.err
}

func (r *serviceTestRepo) DetachContributor(context.Context, int64, int64, ContributorRole) error {
	return r.err
}

func (r *serviceTestRepo) AddCover(context.Context, Cover) (*Cover, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &Cover{LocalPath: "/covers/dune.jpg"}, nil
}

func (r *serviceTestRepo) AttachCover(ctx context.Context, cover Cover) (*Cover, error) {
	return r.AddCover(ctx, cover)
}

func (r *serviceTestRepo) ReplaceCover(context.Context, Cover) (*Cover, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &Cover{LocalPath: "/covers/dune.jpg"}, nil
}

func (r *serviceTestRepo) RemoveCover(context.Context, int64) error {
	return r.err
}

func (r *serviceTestRepo) GetPrimaryCover(context.Context, int64) (*Cover, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &Cover{LocalPath: "/covers/dune.jpg"}, nil
}

func (r *serviceTestRepo) PrimaryCover(ctx context.Context, bookID int64) (*Cover, error) {
	return r.GetPrimaryCover(ctx, bookID)
}

func (r *serviceTestRepo) AddIdentifier(context.Context, Identifier) (*Identifier, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &Identifier{Provider: "isbn", Value: "9780441172719", Scope: IdentifierScopeBook}, nil
}

func (r *serviceTestRepo) FindByIdentifier(context.Context, Identifier) ([]IdentifierMatch, error) {
	if r.err != nil {
		return nil, r.err
	}
	return []IdentifierMatch{{BookID: r.book.ID}}, nil
}
