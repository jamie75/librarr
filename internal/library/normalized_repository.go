package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type NormalizedRepository struct {
	db *sql.DB
}

func NewNormalizedRepository(db *sql.DB) (*NormalizedRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("normalized repository database is required")
	}
	return &NormalizedRepository{db: db}, nil
}

func (r *NormalizedRepository) WithinTransaction(ctx context.Context, fn func(context.Context) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	ctx = context.WithValue(ctx, normalizedTxKey{}, tx)
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

type normalizedTxKey struct{}

func (r *NormalizedRepository) exec(ctx context.Context) sqlExecutor {
	if tx, ok := ctx.Value(normalizedTxKey{}).(*sql.Tx); ok && tx != nil {
		return tx
	}
	return r.db
}

func (r *NormalizedRepository) CreateBook(ctx context.Context, book Book) (*Book, error) {
	if err := book.Validate(); err != nil {
		return nil, err
	}
	if book.SortTitle == "" {
		book.SortTitle = NormalizeKey(book.Title)
	}
	if book.MediaType == "" {
		book.MediaType = MediaTypeEbook
	}
	res, err := r.exec(ctx).ExecContext(ctx, `INSERT INTO books
		(title, sort_title, description, publication_year, language, media_type, monitored, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		book.Title, book.SortTitle, book.Description, nullInt(book.PublicationYear), book.Language,
		string(book.MediaType), boolInt(book.Monitored), string(book.Status))
	if err != nil {
		return nil, mapConstraintError(err)
	}
	book.ID, err = res.LastInsertId()
	if err != nil {
		return nil, mapConstraintError(err)
	}
	return r.GetBook(ctx, book.ID)
}

func (r *NormalizedRepository) UpdateBook(ctx context.Context, book Book) (*Book, error) {
	if book.ID == 0 {
		return nil, fmt.Errorf("%w: book id is required", ErrInvalidDomainObject)
	}
	if err := book.Validate(); err != nil {
		return nil, err
	}
	res, err := r.exec(ctx).ExecContext(ctx, `UPDATE books SET
		title = ?, sort_title = ?, description = ?, publication_year = ?, language = ?,
		media_type = ?, monitored = ?, status = ?, updated_at = datetime('now')
		WHERE id = ?`,
		book.Title, book.SortTitle, book.Description, nullInt(book.PublicationYear), book.Language,
		string(book.MediaType), boolInt(book.Monitored), string(book.Status), book.ID)
	if err != nil {
		return nil, mapConstraintError(err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return nil, ErrNotFound
	}
	return r.GetBook(ctx, book.ID)
}

func (r *NormalizedRepository) SaveBook(ctx context.Context, book Book) (*Book, error) {
	if book.ID == 0 {
		return r.CreateBook(ctx, book)
	}
	return r.UpdateBook(ctx, book)
}

func (r *NormalizedRepository) DeleteBook(ctx context.Context, id int64) error {
	res, err := r.exec(ctx).ExecContext(ctx, `DELETE FROM books WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *NormalizedRepository) GetBook(ctx context.Context, id int64) (*Book, error) {
	row := r.exec(ctx).QueryRowContext(ctx, `SELECT id, title, sort_title, description, publication_year,
		language, media_type, monitored, status, created_at, updated_at FROM books WHERE id = ?`, id)
	book, err := scanBook(row)
	if err != nil {
		return nil, mapConstraintError(err)
	}
	book.Contributors, _ = r.bookContributors(ctx, book.ID)
	book.Series, _ = r.bookSeries(ctx, book.ID)
	book.Identifiers, _ = r.bookIdentifiers(ctx, book.ID)
	book.Covers, _ = r.bookCovers(ctx, book.ID)
	return book, nil
}

func (r *NormalizedRepository) FindBook(ctx context.Context, query BookQuery) (*Book, error) {
	books, err := r.SearchBooks(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(books) == 0 {
		return nil, ErrNotFound
	}
	return &books[0], nil
}

func (r *NormalizedRepository) FindBookByIdentifier(ctx context.Context, identifier Identifier) (*Book, error) {
	matches, err := r.FindByIdentifier(ctx, identifier)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 || matches[0].BookID == 0 {
		return nil, ErrNotFound
	}
	return r.GetBook(ctx, matches[0].BookID)
}

func (r *NormalizedRepository) ListBooks(ctx context.Context, query ListBooksQuery) ([]Book, error) {
	return r.queryBooks(ctx, query)
}

func (r *NormalizedRepository) CountListedBooks(ctx context.Context, query ListBooksQuery) (int, error) {
	where, args := r.listBooksWhere(query)
	var count int
	err := r.exec(ctx).QueryRowContext(ctx, `SELECT COUNT(*) FROM books b`+where, args...).Scan(&count)
	return count, err
}

func (r *NormalizedRepository) SearchBooks(ctx context.Context, query BookQuery) ([]Book, error) {
	return r.queryBooks(ctx, ListBooksQuery{
		MediaType: query.MediaType,
		Search:    query.Title,
		Sort:      "title",
		Order:     "asc",
		Limit:     100,
	})
}

func (r *NormalizedRepository) CountBooks(ctx context.Context, query BookQuery) (int, error) {
	return r.CountListedBooks(ctx, ListBooksQuery{
		MediaType: query.MediaType,
		Search:    query.Title,
	})
}

func (r *NormalizedRepository) RecentBooks(ctx context.Context, query ListBooksQuery) ([]Book, error) {
	if strings.TrimSpace(query.Sort) == "" {
		query.Sort = "recently_added"
	}
	if strings.TrimSpace(query.Order) == "" {
		query.Order = "desc"
	}
	return r.queryBooks(ctx, query)
}

func (r *NormalizedRepository) queryBooks(ctx context.Context, query ListBooksQuery) ([]Book, error) {
	limit := query.Limit
	offset := query.Offset
	if limit <= 0 {
		limit = 100
	}
	where, args := r.listBooksWhere(query)
	orderBy := r.listBooksOrderBy(query)
	args = append(args, limit, offset)
	rows, err := r.exec(ctx).QueryContext(ctx, `SELECT id, title, sort_title, description, publication_year,
		language, media_type, monitored, status, created_at, updated_at FROM books b`+where+`
		ORDER BY `+orderBy+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBooks(rows)
}

func (r *NormalizedRepository) listBooksWhere(query ListBooksQuery) (string, []any) {
	var clauses []string
	var args []any
	search := strings.TrimSpace(query.Search)
	if search != "" {
		like := "%" + search + "%"
		clauses = append(clauses, `(b.title LIKE ? COLLATE NOCASE OR b.sort_title LIKE ? COLLATE NOCASE OR EXISTS (
			SELECT 1
			FROM editions e
			JOIN edition_contributors ec ON ec.edition_id = e.id
			JOIN contributors c ON c.id = ec.contributor_id
			WHERE e.book_id = b.id AND c.name LIKE ? COLLATE NOCASE
		))`)
		args = append(args, like, like, like)
	}
	if query.MediaType != "" {
		clauses = append(clauses, `b.media_type = ?`)
		args = append(args, string(query.MediaType))
	}
	if format := strings.TrimSpace(query.Format); format != "" {
		clauses = append(clauses, `EXISTS (
			SELECT 1
			FROM editions e
			JOIN files f ON f.edition_id = e.id
			WHERE e.book_id = b.id AND f.format = ? COLLATE NOCASE
		)`)
		args = append(args, format)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func (r *NormalizedRepository) listBooksOrderBy(query ListBooksQuery) string {
	sort := strings.ToLower(strings.TrimSpace(query.Sort))
	order := strings.ToUpper(strings.TrimSpace(query.Order))
	if order != "ASC" {
		order = "DESC"
	}
	switch sort {
	case "title":
		return fmt.Sprintf("b.sort_title %s, b.id %s", order, order)
	case "author":
		return fmt.Sprintf(`COALESCE((
			SELECT MIN(c.sort_name)
			FROM editions e
			JOIN edition_contributors ec ON ec.edition_id = e.id
			JOIN contributors c ON c.id = ec.contributor_id
			WHERE e.book_id = b.id AND ec.role = 'author'
		), '') %s, b.sort_title ASC, b.id ASC`, order)
	case "recently_updated":
		return fmt.Sprintf(`MAX(
			b.updated_at,
			COALESCE((SELECT MAX(e.updated_at) FROM editions e WHERE e.book_id = b.id), b.updated_at),
			COALESCE((
				SELECT MAX(f.updated_at)
				FROM files f
				JOIN editions e ON e.id = f.edition_id
				WHERE e.book_id = b.id
			), b.updated_at)
		) %s, b.id %s`, order, order)
	case "", "recently_added":
		return fmt.Sprintf("datetime(b.created_at) %s, b.id %s", order, order)
	default:
		return fmt.Sprintf("datetime(b.created_at) DESC, b.id DESC")
	}
}

func (r *NormalizedRepository) CreateEdition(ctx context.Context, edition Edition) (*Edition, error) {
	if edition.BookID == 0 {
		return nil, fmt.Errorf("%w: edition book is required", ErrInvalidDomainObject)
	}
	if err := edition.Validate(); err != nil {
		return nil, err
	}
	res, err := r.exec(ctx).ExecContext(ctx, `INSERT INTO editions
		(book_id, title, subtitle, publisher, publication_date, language, page_count, edition_name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		edition.BookID, edition.Title, edition.Subtitle, edition.Publisher, edition.PublicationDate,
		edition.Language, nullInt(edition.PageCount), edition.EditionName)
	if err != nil {
		return nil, err
	}
	edition.ID, err = res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return r.GetEdition(ctx, edition.ID)
}

func (r *NormalizedRepository) GetEdition(ctx context.Context, id int64) (*Edition, error) {
	row := r.exec(ctx).QueryRowContext(ctx, `SELECT id, book_id, title, subtitle, publisher,
		publication_date, language, page_count, edition_name, created_at, updated_at FROM editions WHERE id = ?`, id)
	edition, err := scanEdition(row)
	if err != nil {
		return nil, err
	}
	edition.Contributors, _ = r.GetEditionContributors(ctx, edition.ID)
	edition.Identifiers, _ = r.editionIdentifiers(ctx, edition.ID)
	edition.Covers, _ = r.editionCovers(ctx, edition.ID)
	return edition, nil
}

func (r *NormalizedRepository) FindEdition(ctx context.Context, bookID int64, title string) (*Edition, error) {
	row := r.exec(ctx).QueryRowContext(ctx, `SELECT id, book_id, title, subtitle, publisher,
		publication_date, language, page_count, edition_name, created_at, updated_at
		FROM editions WHERE book_id = ? AND title = ? COLLATE NOCASE ORDER BY id LIMIT 1`, bookID, title)
	return scanEdition(row)
}

func (r *NormalizedRepository) ListBookEditions(ctx context.Context, bookID int64) ([]Edition, error) {
	rows, err := r.exec(ctx).QueryContext(ctx, `SELECT id, book_id, title, subtitle, publisher,
		publication_date, language, page_count, edition_name, created_at, updated_at
		FROM editions WHERE book_id = ? ORDER BY id`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEditions(rows)
}

func (r *NormalizedRepository) UpdateEdition(ctx context.Context, edition Edition) (*Edition, error) {
	res, err := r.exec(ctx).ExecContext(ctx, `UPDATE editions SET title = ?, subtitle = ?, publisher = ?,
		publication_date = ?, language = ?, page_count = ?, edition_name = ?, updated_at = datetime('now')
		WHERE id = ?`, edition.Title, edition.Subtitle, edition.Publisher, edition.PublicationDate,
		edition.Language, nullInt(edition.PageCount), edition.EditionName, edition.ID)
	if err != nil {
		return nil, err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return nil, ErrNotFound
	}
	return r.GetEdition(ctx, edition.ID)
}

func (r *NormalizedRepository) DeleteEdition(ctx context.Context, id int64) error {
	res, err := r.exec(ctx).ExecContext(ctx, `DELETE FROM editions WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *NormalizedRepository) AttachFile(ctx context.Context, file BookFile) (*BookFile, error) {
	if err := file.Validate(); err != nil {
		return nil, err
	}
	if file.MediaType == "" {
		file.MediaType = MediaTypeEbook
	}
	res, err := r.exec(ctx).ExecContext(ctx, `INSERT INTO files
		(edition_id, media_type, format, file_path, original_path, file_size, content_hash, source_id,
		 source_type, quality, is_managed, imported_at, embedded_metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		file.EditionID, string(file.MediaType), file.Format, nullableString(file.Path), file.OriginalPath,
		file.Size, file.ContentHash, file.SourceID, file.SourceType, file.Quality, boolInt(file.Managed),
		nullableTime(file.ImportedAt), metadataJSONString(file.EmbeddedMetadataJSON, file.EmbeddedMetadata))
	if err != nil {
		return nil, err
	}
	file.ID, err = res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return r.GetFile(ctx, file.ID)
}

func (r *NormalizedRepository) GetBookFiles(ctx context.Context, bookID int64) ([]BookFile, error) {
	rows, err := r.exec(ctx).QueryContext(ctx, `SELECT f.id, e.book_id, f.edition_id, f.media_type, f.format,
		COALESCE(f.file_path, ''), f.original_path, f.file_size, f.content_hash, f.source_id, f.source_type,
		f.quality, f.is_managed, f.imported_at, f.embedded_metadata_json, f.created_at, f.updated_at
		FROM files f JOIN editions e ON e.id = f.edition_id WHERE e.book_id = ? ORDER BY f.id`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFiles(rows)
}

func (r *NormalizedRepository) ListFiles(ctx context.Context, editionID int64) ([]BookFile, error) {
	rows, err := r.exec(ctx).QueryContext(ctx, `SELECT f.id, e.book_id, f.edition_id, f.media_type, f.format,
		COALESCE(f.file_path, ''), f.original_path, f.file_size, f.content_hash, f.source_id, f.source_type,
		f.quality, f.is_managed, f.imported_at, f.embedded_metadata_json, f.created_at, f.updated_at
		FROM files f JOIN editions e ON e.id = f.edition_id WHERE f.edition_id = ? ORDER BY f.id`, editionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFiles(rows)
}

func (r *NormalizedRepository) GetFile(ctx context.Context, id int64) (*BookFile, error) {
	row := r.exec(ctx).QueryRowContext(ctx, `SELECT f.id, e.book_id, f.edition_id, f.media_type, f.format,
		COALESCE(f.file_path, ''), f.original_path, f.file_size, f.content_hash, f.source_id, f.source_type,
		f.quality, f.is_managed, f.imported_at, f.embedded_metadata_json, f.created_at, f.updated_at
		FROM files f JOIN editions e ON e.id = f.edition_id WHERE f.id = ?`, id)
	return scanFile(row)
}

func (r *NormalizedRepository) FindFileByPath(ctx context.Context, path string) (*BookFile, error) {
	return r.FindByPath(ctx, path)
}

func (r *NormalizedRepository) FindByPath(ctx context.Context, path string) (*BookFile, error) {
	row := r.exec(ctx).QueryRowContext(ctx, `SELECT f.id, e.book_id, f.edition_id, f.media_type, f.format,
		COALESCE(f.file_path, ''), f.original_path, f.file_size, f.content_hash, f.source_id, f.source_type,
		f.quality, f.is_managed, f.imported_at, f.embedded_metadata_json, f.created_at, f.updated_at
		FROM files f JOIN editions e ON e.id = f.edition_id WHERE f.file_path = ?`, path)
	return scanFile(row)
}

func (r *NormalizedRepository) FindFileBySourceID(ctx context.Context, sourceID string) (*BookFile, error) {
	row := r.exec(ctx).QueryRowContext(ctx, `SELECT f.id, e.book_id, f.edition_id, f.media_type, f.format,
		COALESCE(f.file_path, ''), f.original_path, f.file_size, f.content_hash, f.source_id, f.source_type,
		f.quality, f.is_managed, f.imported_at, f.embedded_metadata_json, f.created_at, f.updated_at
		FROM files f JOIN editions e ON e.id = f.edition_id WHERE f.source_id = ? ORDER BY f.id LIMIT 1`, sourceID)
	return scanFile(row)
}

func (r *NormalizedRepository) FindFilesByContentHash(ctx context.Context, hash string) ([]BookFile, error) {
	rows, err := r.exec(ctx).QueryContext(ctx, `SELECT f.id, e.book_id, f.edition_id, f.media_type, f.format,
		COALESCE(f.file_path, ''), f.original_path, f.file_size, f.content_hash, f.source_id, f.source_type,
		f.quality, f.is_managed, f.imported_at, f.embedded_metadata_json, f.created_at, f.updated_at
		FROM files f JOIN editions e ON e.id = f.edition_id WHERE f.content_hash = ? ORDER BY f.id`, hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFiles(rows)
}

func (r *NormalizedRepository) DetachFile(ctx context.Context, id int64) error {
	return r.DeleteFile(ctx, id)
}

func (r *NormalizedRepository) MoveFile(ctx context.Context, id int64, path string) (*BookFile, error) {
	res, err := r.exec(ctx).ExecContext(ctx, `UPDATE files SET file_path = ?, updated_at = datetime('now') WHERE id = ?`, path, id)
	if err != nil {
		return nil, err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return nil, ErrNotFound
	}
	return r.GetFile(ctx, id)
}

func (r *NormalizedRepository) DeleteFile(ctx context.Context, id int64) error {
	res, err := r.exec(ctx).ExecContext(ctx, `DELETE FROM files WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *NormalizedRepository) ValidateManagedFile(ctx context.Context, id int64) error {
	file, err := r.GetFile(ctx, id)
	if err != nil {
		return err
	}
	if !file.Managed {
		return ErrUnsupportedOperation
	}
	if strings.TrimSpace(file.Path) == "" {
		return ErrUnsafePath
	}
	if _, err := os.Stat(file.Path); err != nil {
		return err
	}
	return nil
}

func (r *NormalizedRepository) MergeContributor(ctx context.Context, contributor Contributor) (*Contributor, error) {
	if err := contributor.Validate(); err != nil {
		return nil, err
	}
	sortName := contributor.SortName
	if sortName == "" {
		sortName = NormalizeKey(contributor.Name)
	}
	row := r.exec(ctx).QueryRowContext(ctx, `SELECT id, name, sort_name, created_at, updated_at
		FROM contributors WHERE name = ? COLLATE NOCASE OR sort_name = ? COLLATE NOCASE ORDER BY id LIMIT 1`,
		contributor.Name, sortName)
	existing, err := scanContributor(row)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	res, err := r.exec(ctx).ExecContext(ctx, `INSERT INTO contributors (name, sort_name) VALUES (?, ?)`,
		contributor.Name, sortName)
	if err != nil {
		return nil, err
	}
	contributor.ID, err = res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return r.getContributor(ctx, contributor.ID)
}

func (r *NormalizedRepository) AttachContributor(ctx context.Context, editionID int64, contributor Contributor) error {
	merged, err := r.MergeContributor(ctx, contributor)
	if err != nil {
		return err
	}
	roles := contributor.Roles
	if len(roles) == 0 {
		roles = []ContributorRole{RoleAuthor}
	}
	for i, role := range roles {
		position := contributor.Position
		if position == 0 {
			position = i
		}
		if _, err := r.exec(ctx).ExecContext(ctx, `INSERT OR IGNORE INTO edition_contributors
			(edition_id, contributor_id, role, position) VALUES (?, ?, ?, ?)`,
			editionID, merged.ID, string(role), position); err != nil {
			return err
		}
	}
	return nil
}

func (r *NormalizedRepository) DetachContributor(ctx context.Context, editionID, contributorID int64, role ContributorRole) error {
	_, err := r.exec(ctx).ExecContext(ctx, `DELETE FROM edition_contributors WHERE edition_id = ? AND contributor_id = ? AND role = ?`,
		editionID, contributorID, string(role))
	return err
}

func (r *NormalizedRepository) GetEditionContributors(ctx context.Context, editionID int64) ([]Contributor, error) {
	rows, err := r.exec(ctx).QueryContext(ctx, `SELECT c.id, c.name, c.sort_name, ec.role, ec.position, c.created_at, c.updated_at
		FROM edition_contributors ec JOIN contributors c ON c.id = ec.contributor_id
		WHERE ec.edition_id = ? ORDER BY ec.position, c.id`, editionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var contributors []Contributor
	for rows.Next() {
		var c Contributor
		var role string
		var created, updated string
		if err := rows.Scan(&c.ID, &c.Name, &c.SortName, &role, &c.Position, &created, &updated); err != nil {
			return nil, err
		}
		c.Roles = []ContributorRole{ContributorRole(role)}
		c.CreatedAt = parseTime(created)
		c.UpdatedAt = parseTime(updated)
		contributors = append(contributors, c)
	}
	return contributors, rows.Err()
}

func (r *NormalizedRepository) getContributor(ctx context.Context, id int64) (*Contributor, error) {
	row := r.exec(ctx).QueryRowContext(ctx, `SELECT id, name, sort_name, created_at, updated_at FROM contributors WHERE id = ?`, id)
	return scanContributor(row)
}

func (r *NormalizedRepository) GetSeries(ctx context.Context, title string) (*Series, error) {
	row := r.exec(ctx).QueryRowContext(ctx, `SELECT id, title, description, created_at, updated_at FROM series
		WHERE title = ? COLLATE NOCASE ORDER BY id LIMIT 1`, title)
	return scanSeries(row)
}

func (r *NormalizedRepository) FindSeries(ctx context.Context, title string) ([]Series, error) {
	rows, err := r.exec(ctx).QueryContext(ctx, `SELECT id, title, description, created_at, updated_at FROM series
		WHERE title LIKE ? COLLATE NOCASE ORDER BY title`, "%"+strings.TrimSpace(title)+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSeriesRows(rows)
}

func (r *NormalizedRepository) AttachBookToSeries(ctx context.Context, bookID int64, series BookSeries) error {
	return r.AttachBook(ctx, bookID, series)
}

func (r *NormalizedRepository) AttachBook(ctx context.Context, bookID int64, link BookSeries) error {
	series := link.Series
	if series.ID == 0 {
		found, err := r.GetSeries(ctx, series.Title)
		if err == nil {
			series = *found
		} else if errors.Is(err, ErrNotFound) {
			created, err := r.createSeries(ctx, series)
			if err != nil {
				return err
			}
			series = *created
		} else {
			return err
		}
	}
	_, err := r.exec(ctx).ExecContext(ctx, `INSERT INTO book_series (book_id, series_id, position, display_position)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(book_id, series_id) DO UPDATE SET position = excluded.position, display_position = excluded.display_position`,
		bookID, series.ID, nullFloat(link.Position), link.DisplayPosition)
	return err
}

func (r *NormalizedRepository) DetachBook(ctx context.Context, bookID, seriesID int64) error {
	_, err := r.exec(ctx).ExecContext(ctx, `DELETE FROM book_series WHERE book_id = ? AND series_id = ?`, bookID, seriesID)
	return err
}

func (r *NormalizedRepository) SeriesPosition(ctx context.Context, bookID, seriesID int64) (BookSeries, error) {
	row := r.exec(ctx).QueryRowContext(ctx, `SELECT s.id, s.title, s.description, s.created_at, s.updated_at,
		bs.position, bs.display_position FROM book_series bs JOIN series s ON s.id = bs.series_id
		WHERE bs.book_id = ? AND bs.series_id = ?`, bookID, seriesID)
	return scanBookSeries(row)
}

func (r *NormalizedRepository) ListSeriesBooks(ctx context.Context, seriesID int64) ([]Book, error) {
	rows, err := r.exec(ctx).QueryContext(ctx, `SELECT b.id, b.title, b.sort_title, b.description, b.publication_year,
		b.language, b.media_type, b.monitored, b.status, b.created_at, b.updated_at
		FROM book_series bs JOIN books b ON b.id = bs.book_id WHERE bs.series_id = ?
		ORDER BY bs.position, b.sort_title`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBooks(rows)
}

func (r *NormalizedRepository) createSeries(ctx context.Context, series Series) (*Series, error) {
	if err := series.Validate(); err != nil {
		return nil, err
	}
	res, err := r.exec(ctx).ExecContext(ctx, `INSERT INTO series (title, description) VALUES (?, ?)`,
		series.Title, series.Description)
	if err != nil {
		return nil, err
	}
	series.ID, err = res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return r.GetSeries(ctx, series.Title)
}

func (r *NormalizedRepository) AddIdentifier(ctx context.Context, identifier Identifier) (*Identifier, error) {
	if err := identifier.Validate(); err != nil {
		return nil, err
	}
	var bookID, editionID any
	if identifier.Scope == IdentifierScopeBook {
		bookID = identifier.Source
		if parsed := parseInt64(identifier.Source); parsed != 0 {
			bookID = parsed
		}
	} else {
		editionID = identifier.Source
		if parsed := parseInt64(identifier.Source); parsed != 0 {
			editionID = parsed
		}
	}
	res, err := r.exec(ctx).ExecContext(ctx, `INSERT INTO identifiers (book_id, edition_id, provider, identifier) VALUES (?, ?, ?, ?)`,
		bookID, editionID, identifier.Provider, identifier.Value)
	if err != nil {
		return nil, mapConstraintError(err)
	}
	identifier.ID, err = res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &identifier, nil
}

func (r *NormalizedRepository) FindByIdentifier(ctx context.Context, identifier Identifier) ([]IdentifierMatch, error) {
	rows, err := r.exec(ctx).QueryContext(ctx, `SELECT i.id, i.book_id, i.edition_id, i.provider, i.identifier, i.created_at,
		COALESCE(e.book_id, i.book_id) AS resolved_book_id
		FROM identifiers i LEFT JOIN editions e ON e.id = i.edition_id
		WHERE i.provider = ? COLLATE NOCASE AND i.identifier = ? COLLATE NOCASE`,
		identifier.Provider, identifier.Value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var matches []IdentifierMatch
	for rows.Next() {
		var id Identifier
		var bookID, editionID, resolved sql.NullInt64
		var created string
		if err := rows.Scan(&id.ID, &bookID, &editionID, &id.Provider, &id.Value, &created, &resolved); err != nil {
			return nil, err
		}
		id.CreatedAt = parseTime(created)
		id.Scope = IdentifierScopeBook
		if editionID.Valid {
			id.Scope = IdentifierScopeEdition
		}
		matches = append(matches, IdentifierMatch{BookID: resolved.Int64, EditionID: editionID.Int64, Identifier: id})
	}
	return matches, rows.Err()
}

func (r *NormalizedRepository) AddCover(ctx context.Context, cover Cover) (*Cover, error) {
	return r.AttachCover(ctx, cover)
}

func (r *NormalizedRepository) AttachCover(ctx context.Context, cover Cover) (*Cover, error) {
	if err := cover.Validate(); err != nil {
		return nil, err
	}
	if cover.IsPrimary {
		if err := r.clearPrimaryCover(ctx, cover.BookID, cover.EditionID); err != nil {
			return nil, err
		}
	}
	res, err := r.exec(ctx).ExecContext(ctx, `INSERT INTO covers
		(book_id, edition_id, source, source_url, local_path, mime_type, width, height, is_primary)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullableID(cover.BookID), nullableID(cover.EditionID), cover.Source, cover.SourceURL, cover.LocalPath,
		cover.MimeType, nullInt(cover.Width), nullInt(cover.Height), boolInt(cover.IsPrimary))
	if err != nil {
		return nil, err
	}
	cover.ID, err = res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return r.getCover(ctx, cover.ID)
}

func (r *NormalizedRepository) ReplaceCover(ctx context.Context, cover Cover) (*Cover, error) {
	if cover.ID == 0 {
		return r.AttachCover(ctx, cover)
	}
	if cover.IsPrimary {
		if err := r.clearPrimaryCover(ctx, cover.BookID, cover.EditionID); err != nil {
			return nil, err
		}
	}
	res, err := r.exec(ctx).ExecContext(ctx, `UPDATE covers SET source = ?, source_url = ?, local_path = ?,
		mime_type = ?, width = ?, height = ?, is_primary = ?, updated_at = datetime('now') WHERE id = ?`,
		cover.Source, cover.SourceURL, cover.LocalPath, cover.MimeType, nullInt(cover.Width), nullInt(cover.Height),
		boolInt(cover.IsPrimary), cover.ID)
	if err != nil {
		return nil, err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return nil, ErrNotFound
	}
	return r.getCover(ctx, cover.ID)
}

func (r *NormalizedRepository) RemoveCover(ctx context.Context, id int64) error {
	_, err := r.exec(ctx).ExecContext(ctx, `DELETE FROM covers WHERE id = ?`, id)
	return err
}

func (r *NormalizedRepository) GetPrimaryCover(ctx context.Context, bookID int64) (*Cover, error) {
	return r.PrimaryCover(ctx, bookID)
}

func (r *NormalizedRepository) PrimaryCover(ctx context.Context, bookID int64) (*Cover, error) {
	row := r.exec(ctx).QueryRowContext(ctx, `SELECT id, book_id, edition_id, source, source_url, local_path,
		mime_type, width, height, is_primary, created_at, updated_at FROM covers
		WHERE book_id = ? AND is_primary = 1 ORDER BY id LIMIT 1`, bookID)
	return scanCover(row)
}

func (r *NormalizedRepository) getCover(ctx context.Context, id int64) (*Cover, error) {
	row := r.exec(ctx).QueryRowContext(ctx, `SELECT id, book_id, edition_id, source, source_url, local_path,
		mime_type, width, height, is_primary, created_at, updated_at FROM covers WHERE id = ?`, id)
	return scanCover(row)
}

func (r *NormalizedRepository) clearPrimaryCover(ctx context.Context, bookID, editionID int64) error {
	if bookID != 0 {
		_, err := r.exec(ctx).ExecContext(ctx, `UPDATE covers SET is_primary = 0 WHERE book_id = ?`, bookID)
		return err
	}
	if editionID != 0 {
		_, err := r.exec(ctx).ExecContext(ctx, `UPDATE covers SET is_primary = 0 WHERE edition_id = ?`, editionID)
		return err
	}
	return nil
}

func (r *NormalizedRepository) SaveEmbeddedMetadata(ctx context.Context, fileID int64, metadata map[string]string) error {
	res, err := r.exec(ctx).ExecContext(ctx, `UPDATE files SET embedded_metadata_json = ?, updated_at = datetime('now') WHERE id = ?`, metadataJSON(metadata), fileID)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *NormalizedRepository) SaveProviderMetadata(context.Context, int64, string, map[string]string) error {
	return ErrUnsupportedOperation
}

func (r *NormalizedRepository) SaveUserOverride(context.Context, int64, string, string) error {
	return ErrUnsupportedOperation
}

func scanBook(scanner interface{ Scan(...any) error }) (*Book, error) {
	var b Book
	var pub sql.NullInt64
	var media, status, created, updated string
	var monitored int
	err := scanner.Scan(&b.ID, &b.Title, &b.SortTitle, &b.Description, &pub, &b.Language,
		&media, &monitored, &status, &created, &updated)
	if err != nil {
		return nil, mapSQLError(err)
	}
	if pub.Valid {
		b.PublicationYear = int(pub.Int64)
	}
	b.MediaType = MediaType(media)
	b.Monitored = monitored != 0
	b.Status = BookStatus(status)
	b.CreatedAt = parseTime(created)
	b.UpdatedAt = parseTime(updated)
	return &b, nil
}

func scanBooks(rows *sql.Rows) ([]Book, error) {
	var books []Book
	for rows.Next() {
		book, err := scanBook(rows)
		if err != nil {
			return nil, err
		}
		books = append(books, *book)
	}
	return books, rows.Err()
}

func scanEdition(scanner interface{ Scan(...any) error }) (*Edition, error) {
	var e Edition
	var pages sql.NullInt64
	var created, updated string
	err := scanner.Scan(&e.ID, &e.BookID, &e.Title, &e.Subtitle, &e.Publisher, &e.PublicationDate,
		&e.Language, &pages, &e.EditionName, &created, &updated)
	if err != nil {
		return nil, mapSQLError(err)
	}
	if pages.Valid {
		e.PageCount = int(pages.Int64)
	}
	e.CreatedAt = parseTime(created)
	e.UpdatedAt = parseTime(updated)
	return &e, nil
}

func scanEditions(rows *sql.Rows) ([]Edition, error) {
	var editions []Edition
	for rows.Next() {
		edition, err := scanEdition(rows)
		if err != nil {
			return nil, err
		}
		editions = append(editions, *edition)
	}
	return editions, rows.Err()
}

func scanFile(scanner interface{ Scan(...any) error }) (*BookFile, error) {
	var f BookFile
	var media, imported, created, updated string
	var metadata string
	var managed int
	var importedNull sql.NullString
	err := scanner.Scan(&f.ID, &f.BookID, &f.EditionID, &media, &f.Format, &f.Path, &f.OriginalPath,
		&f.Size, &f.ContentHash, &f.SourceID, &f.SourceType, &f.Quality, &managed, &importedNull, &metadata, &created, &updated)
	if err != nil {
		return nil, mapSQLError(err)
	}
	if importedNull.Valid {
		imported = importedNull.String
	}
	f.MediaType = MediaType(media)
	f.Managed = managed != 0
	f.EmbeddedMetadataJSON = metadata
	f.EmbeddedMetadata = parseMetadataJSON(metadata)
	f.ImportedAt = parseTime(imported)
	f.CreatedAt = parseTime(created)
	f.UpdatedAt = parseTime(updated)
	return &f, nil
}

func scanFiles(rows *sql.Rows) ([]BookFile, error) {
	var files []BookFile
	for rows.Next() {
		file, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		files = append(files, *file)
	}
	return files, rows.Err()
}

func scanContributor(scanner interface{ Scan(...any) error }) (*Contributor, error) {
	var c Contributor
	var created, updated string
	if err := scanner.Scan(&c.ID, &c.Name, &c.SortName, &created, &updated); err != nil {
		return nil, mapSQLError(err)
	}
	c.CreatedAt = parseTime(created)
	c.UpdatedAt = parseTime(updated)
	return &c, nil
}

func scanSeries(scanner interface{ Scan(...any) error }) (*Series, error) {
	var s Series
	var created, updated string
	if err := scanner.Scan(&s.ID, &s.Title, &s.Description, &created, &updated); err != nil {
		return nil, mapSQLError(err)
	}
	s.CreatedAt = parseTime(created)
	s.UpdatedAt = parseTime(updated)
	return &s, nil
}

func scanSeriesRows(rows *sql.Rows) ([]Series, error) {
	var series []Series
	for rows.Next() {
		s, err := scanSeries(rows)
		if err != nil {
			return nil, err
		}
		series = append(series, *s)
	}
	return series, rows.Err()
}

func scanBookSeries(scanner interface{ Scan(...any) error }) (BookSeries, error) {
	var bs BookSeries
	var position sql.NullFloat64
	var created, updated string
	if err := scanner.Scan(&bs.Series.ID, &bs.Series.Title, &bs.Series.Description, &created, &updated,
		&position, &bs.DisplayPosition); err != nil {
		return BookSeries{}, mapSQLError(err)
	}
	if position.Valid {
		bs.Position = position.Float64
	}
	bs.Series.CreatedAt = parseTime(created)
	bs.Series.UpdatedAt = parseTime(updated)
	return bs, nil
}

func scanCover(scanner interface{ Scan(...any) error }) (*Cover, error) {
	var c Cover
	var bookID, editionID, width, height sql.NullInt64
	var primary int
	var created, updated string
	if err := scanner.Scan(&c.ID, &bookID, &editionID, &c.Source, &c.SourceURL, &c.LocalPath,
		&c.MimeType, &width, &height, &primary, &created, &updated); err != nil {
		return nil, mapSQLError(err)
	}
	c.BookID = bookID.Int64
	c.EditionID = editionID.Int64
	if width.Valid {
		c.Width = int(width.Int64)
	}
	if height.Valid {
		c.Height = int(height.Int64)
	}
	c.IsPrimary = primary != 0
	c.CreatedAt = parseTime(created)
	c.UpdatedAt = parseTime(updated)
	return &c, nil
}

func (r *NormalizedRepository) bookContributors(ctx context.Context, bookID int64) ([]Contributor, error) {
	rows, err := r.exec(ctx).QueryContext(ctx, `SELECT DISTINCT c.id, c.name, c.sort_name, c.created_at, c.updated_at
		FROM contributors c JOIN edition_contributors ec ON ec.contributor_id = c.id
		JOIN editions e ON e.id = ec.edition_id WHERE e.book_id = ? ORDER BY ec.position, c.id`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var contributors []Contributor
	for rows.Next() {
		c, err := scanContributor(rows)
		if err != nil {
			return nil, err
		}
		contributors = append(contributors, *c)
	}
	return contributors, rows.Err()
}

func (r *NormalizedRepository) bookSeries(ctx context.Context, bookID int64) ([]BookSeries, error) {
	rows, err := r.exec(ctx).QueryContext(ctx, `SELECT s.id, s.title, s.description, s.created_at, s.updated_at,
		bs.position, bs.display_position FROM book_series bs JOIN series s ON s.id = bs.series_id
		WHERE bs.book_id = ? ORDER BY bs.position, s.title`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var series []BookSeries
	for rows.Next() {
		bs, err := scanBookSeries(rows)
		if err != nil {
			return nil, err
		}
		series = append(series, bs)
	}
	return series, rows.Err()
}

func (r *NormalizedRepository) bookIdentifiers(ctx context.Context, bookID int64) ([]Identifier, error) {
	rows, err := r.exec(ctx).QueryContext(ctx, `SELECT id, provider, identifier, created_at FROM identifiers WHERE book_id = ? ORDER BY id`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIdentifierRows(rows, IdentifierScopeBook)
}

func (r *NormalizedRepository) editionIdentifiers(ctx context.Context, editionID int64) ([]Identifier, error) {
	rows, err := r.exec(ctx).QueryContext(ctx, `SELECT id, provider, identifier, created_at FROM identifiers WHERE edition_id = ? ORDER BY id`, editionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIdentifierRows(rows, IdentifierScopeEdition)
}

func scanIdentifierRows(rows *sql.Rows, scope IdentifierScope) ([]Identifier, error) {
	var identifiers []Identifier
	for rows.Next() {
		var id Identifier
		var created string
		if err := rows.Scan(&id.ID, &id.Provider, &id.Value, &created); err != nil {
			return nil, err
		}
		id.Scope = scope
		id.CreatedAt = parseTime(created)
		identifiers = append(identifiers, id)
	}
	return identifiers, rows.Err()
}

func (r *NormalizedRepository) bookCovers(ctx context.Context, bookID int64) ([]Cover, error) {
	return r.covers(ctx, `book_id = ?`, bookID)
}

func (r *NormalizedRepository) editionCovers(ctx context.Context, editionID int64) ([]Cover, error) {
	return r.covers(ctx, `edition_id = ?`, editionID)
}

func (r *NormalizedRepository) covers(ctx context.Context, where string, arg any) ([]Cover, error) {
	rows, err := r.exec(ctx).QueryContext(ctx, `SELECT id, book_id, edition_id, source, source_url, local_path,
		mime_type, width, height, is_primary, created_at, updated_at FROM covers WHERE `+where+` ORDER BY is_primary DESC, id`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var covers []Cover
	for rows.Next() {
		cover, err := scanCover(rows)
		if err != nil {
			return nil, err
		}
		covers = append(covers, *cover)
	}
	return covers, rows.Err()
}

func mapSQLError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func mapConstraintError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
		return fmt.Errorf("%w: %v", ErrDuplicateBook, err)
	}
	return err
}

func parseTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t
		}
	}
	return time.Time{}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullableID(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullFloat(v float64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullableString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func nullableTime(v time.Time) any {
	if v.IsZero() {
		return nil
	}
	return v.UTC().Format("2006-01-02 15:04:05")
}

func metadataJSON(metadata map[string]string) string {
	if len(metadata) == 0 {
		return "{}"
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func metadataJSONString(raw string, metadata map[string]string) string {
	raw = strings.TrimSpace(raw)
	if raw != "" && json.Valid([]byte(raw)) {
		return raw
	}
	return metadataJSON(metadata)
}

func parseMetadataJSON(value string) map[string]string {
	value = strings.TrimSpace(value)
	if value == "" || value == "{}" {
		return nil
	}
	var metadata map[string]string
	if err := json.Unmarshal([]byte(value), &metadata); err != nil {
		return nil
	}
	return metadata
}

func parseInt64(v string) int64 {
	var id int64
	_, _ = fmt.Sscanf(strings.TrimSpace(v), "%d", &id)
	return id
}

var _ LibraryRepository = (*NormalizedRepository)(nil)
var _ EditionRepository = (*NormalizedRepository)(nil)
var _ TransactionManager = (*NormalizedRepository)(nil)
