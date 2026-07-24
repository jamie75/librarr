package library

import "context"

type BookRepository interface {
	FindBook(context.Context, BookQuery) (*Book, error)
	FindBookByIdentifier(context.Context, Identifier) (*Book, error)
	GetBook(context.Context, int64) (*Book, error)
	ListBooks(context.Context, ListBooksQuery) ([]Book, error)
	SaveBook(context.Context, Book) (*Book, error)
	DeleteBook(context.Context, int64) error
}

type FileRepository interface {
	GetBookFiles(context.Context, int64) ([]BookFile, error)
	GetFile(context.Context, int64) (*BookFile, error)
	FindFileByPath(context.Context, string) (*BookFile, error)
	FindFileBySourceID(context.Context, string) (*BookFile, error)
	FindFilesByContentHash(context.Context, string) ([]BookFile, error)
	AttachFile(context.Context, BookFile) (*BookFile, error)
	DetachFile(context.Context, int64) error
}

type SeriesRepository interface {
	GetSeries(context.Context, string) (*Series, error)
	AttachBookToSeries(context.Context, int64, BookSeries) error
}

type ContributorRepository interface {
	MergeContributor(context.Context, Contributor) (*Contributor, error)
	GetEditionContributors(context.Context, int64) ([]Contributor, error)
	AttachContributor(context.Context, int64, Contributor) error
}

type MetadataRepository interface {
	SaveEmbeddedMetadata(context.Context, int64, map[string]string) error
	SaveProviderMetadata(context.Context, int64, string, map[string]string) error
	SaveUserOverride(context.Context, int64, string, string) error
}

type CoverRepository interface {
	AddCover(context.Context, Cover) (*Cover, error)
	GetPrimaryCover(context.Context, int64) (*Cover, error)
}

type IdentifierRepository interface {
	AddIdentifier(context.Context, Identifier) (*Identifier, error)
	FindByIdentifier(context.Context, Identifier) ([]IdentifierMatch, error)
}

type LibraryRepository interface {
	BookRepository
	FileRepository
	SeriesRepository
	ContributorRepository
	CoverRepository
	IdentifierRepository
}

type IdentifierMatch struct {
	BookID     int64
	EditionID  int64
	Identifier Identifier
}

type ListBooksQuery struct {
	MediaType MediaType
	Limit     int
	Offset    int
}
