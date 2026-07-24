package library

import (
	"context"
	"errors"
	"fmt"

	"github.com/JeremiahM37/librarr/internal/db"
	"github.com/JeremiahM37/librarr/internal/models"
)

type TransactionManager interface {
	WithinTransaction(context.Context, func(context.Context) error) error
}

type NoopTransactionManager struct{}

func (NoopTransactionManager) WithinTransaction(ctx context.Context, fn func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(ctx)
}

type LegacyCompatibilityRepository interface {
	ListLegacyItems(context.Context, string, int, int) ([]models.LibraryItem, error)
	CountLegacyItems(context.Context, string) (int, error)
	LegacyStats(context.Context) (map[string]interface{}, error)
	GetLegacyItem(context.Context, int64) (models.LibraryItem, error)
	DeleteLegacyItem(context.Context, int64) error
	DeleteLegacyItemBySourceID(context.Context, string) error
	HasLegacySourceID(context.Context, string) (bool, error)
}

type ServiceOptions struct {
	BookRepository        BookRepository
	FileRepository        FileRepository
	MetadataRepository    MetadataRepository
	SeriesRepository      SeriesRepository
	ContributorRepository ContributorRepository
	IdentifierRepository  IdentifierRepository
	CoverRepository       CoverRepository
	MatchingService       MatchingService
	FileService           FileService
	TransactionManager    TransactionManager
	LegacyCompatibility   LegacyCompatibilityRepository
}

type LibraryService struct {
	books        BookRepository
	files        FileRepository
	metadata     MetadataRepository
	series       SeriesRepository
	contributors ContributorRepository
	identifiers  IdentifierRepository
	covers       CoverRepository
	matcher      MatchingService
	fileService  FileService
	transactions TransactionManager
	legacy       LegacyCompatibilityRepository
}

func NewLibraryService(opts ServiceOptions) (*LibraryService, error) {
	if opts.BookRepository == nil {
		return nil, fmt.Errorf("book repository is required")
	}
	if opts.FileRepository == nil {
		return nil, fmt.Errorf("file repository is required")
	}
	if opts.SeriesRepository == nil {
		return nil, fmt.Errorf("series repository is required")
	}
	if opts.ContributorRepository == nil {
		return nil, fmt.Errorf("contributor repository is required")
	}
	if opts.IdentifierRepository == nil {
		return nil, fmt.Errorf("identifier repository is required")
	}
	if opts.CoverRepository == nil {
		return nil, fmt.Errorf("cover repository is required")
	}
	if opts.TransactionManager == nil {
		opts.TransactionManager = NoopTransactionManager{}
	}
	return &LibraryService{
		books:        opts.BookRepository,
		files:        opts.FileRepository,
		metadata:     opts.MetadataRepository,
		series:       opts.SeriesRepository,
		contributors: opts.ContributorRepository,
		identifiers:  opts.IdentifierRepository,
		covers:       opts.CoverRepository,
		matcher:      opts.MatchingService,
		fileService:  opts.FileService,
		transactions: opts.TransactionManager,
		legacy:       opts.LegacyCompatibility,
	}, nil
}

func NewLegacyLibraryService(database *db.DB) (*LibraryService, error) {
	repo := NewLegacyLibraryRepository(database)
	return NewLibraryService(ServiceOptions{
		BookRepository:        repo,
		FileRepository:        repo,
		SeriesRepository:      repo,
		ContributorRepository: repo,
		IdentifierRepository:  repo,
		CoverRepository:       repo,
		LegacyCompatibility:   repo,
	})
}

func (s *LibraryService) FindBook(ctx context.Context, query BookQuery) (*Book, error) {
	book, err := s.books.FindBook(ctx, query)
	return book, translateLibraryError(err)
}

func (s *LibraryService) FindBookByID(ctx context.Context, id int64) (*Book, error) {
	book, err := s.books.GetBook(ctx, id)
	return book, translateLibraryError(err)
}

func (s *LibraryService) FindBookByIdentifier(ctx context.Context, identifier Identifier) (*Book, error) {
	if err := identifier.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidIdentifier, err)
	}
	book, err := s.books.FindBookByIdentifier(ctx, identifier)
	return book, translateLibraryError(err)
}

func (s *LibraryService) SearchBooks(ctx context.Context, query BookQuery) ([]Book, error) {
	books, err := s.books.SearchBooks(ctx, query)
	return books, translateLibraryError(err)
}

func (s *LibraryService) ListBooks(ctx context.Context, query ListBooksQuery) ([]Book, error) {
	books, err := s.books.ListBooks(ctx, query)
	return books, translateLibraryError(err)
}

func (s *LibraryService) ListRecent(ctx context.Context, mediaType MediaType, limit, offset int) ([]Book, error) {
	books, err := s.books.RecentBooks(ctx, ListBooksQuery{MediaType: mediaType, Limit: limit, Offset: offset})
	return books, translateLibraryError(err)
}

func (s *LibraryService) GetBookFiles(ctx context.Context, bookID int64) ([]BookFile, error) {
	files, err := s.files.GetBookFiles(ctx, bookID)
	return files, translateLibraryError(err)
}

func (s *LibraryService) GetSeries(ctx context.Context, name string) (*Series, error) {
	series, err := s.series.GetSeries(ctx, name)
	return series, translateLibraryError(err)
}

func (s *LibraryService) DeleteBook(ctx context.Context, id int64) error {
	return translateLibraryError(s.books.DeleteBook(ctx, id))
}

func (s *LibraryService) RefreshBook(context.Context, int64) error {
	return ErrUnsupportedOperation
}

func (s *LibraryService) AttachFile(ctx context.Context, file BookFile) (*BookFile, error) {
	attached, err := s.files.AttachFile(ctx, file)
	return attached, translateLibraryError(err)
}

func (s *LibraryService) DetachFile(ctx context.Context, fileID int64) error {
	return translateLibraryError(s.files.DetachFile(ctx, fileID))
}

func (s *LibraryService) ImportCandidate(context.Context, ImportCandidate) (ImportResult, error) {
	return ImportResult{}, ErrUnsupportedOperation
}

func (s *LibraryService) ListLegacyItems(ctx context.Context, mediaType string, limit, offset int) ([]models.LibraryItem, error) {
	if s.legacy == nil {
		return nil, ErrUnsupportedOperation
	}
	items, err := s.legacy.ListLegacyItems(ctx, mediaType, limit, offset)
	return items, translateLibraryError(err)
}

func (s *LibraryService) CountLegacyItems(ctx context.Context, mediaType string) (int, error) {
	if s.legacy == nil {
		return 0, ErrUnsupportedOperation
	}
	count, err := s.legacy.CountLegacyItems(ctx, mediaType)
	return count, translateLibraryError(err)
}

func (s *LibraryService) LegacyStats(ctx context.Context) (map[string]interface{}, error) {
	if s.legacy == nil {
		return nil, ErrUnsupportedOperation
	}
	stats, err := s.legacy.LegacyStats(ctx)
	return stats, translateLibraryError(err)
}

func (s *LibraryService) GetLegacyItem(ctx context.Context, id int64) (models.LibraryItem, error) {
	if s.legacy == nil {
		return models.LibraryItem{}, ErrUnsupportedOperation
	}
	item, err := s.legacy.GetLegacyItem(ctx, id)
	return item, translateLibraryError(err)
}

func (s *LibraryService) DeleteLegacyItem(ctx context.Context, id int64) error {
	if s.legacy == nil {
		return ErrUnsupportedOperation
	}
	return translateLibraryError(s.legacy.DeleteLegacyItem(ctx, id))
}

func (s *LibraryService) DeleteLegacyItemBySourceID(ctx context.Context, sourceID string) error {
	if s.legacy == nil {
		return ErrUnsupportedOperation
	}
	return translateLibraryError(s.legacy.DeleteLegacyItemBySourceID(ctx, sourceID))
}

func (s *LibraryService) HasLegacySourceID(ctx context.Context, sourceID string) (bool, error) {
	if s.legacy == nil {
		return false, ErrUnsupportedOperation
	}
	ok, err := s.legacy.HasLegacySourceID(ctx, sourceID)
	return ok, translateLibraryError(err)
}

func translateLibraryError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		return ErrBookNotFound
	}
	if errors.Is(err, ErrReadOnlyRepository) {
		return ErrRepositoryReadOnly
	}
	return err
}
