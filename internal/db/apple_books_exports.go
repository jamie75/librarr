package db

import (
	"database/sql"

	"github.com/jamie75/librarr/internal/models"
)

const appleBooksExportColumns = `id, book_id, media_type, requested_format, actual_format, status,
	source_file_count, source_bytes, destination_path, destination_name, checksum, error,
	created_at, completed_at, updated_at`

func (d *DB) CreateAppleBooksExport(export *models.AppleBooksExport) (int64, error) {
	if export == nil {
		return 0, sql.ErrNoRows
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	result, err := d.db.Exec(`INSERT INTO apple_books_exports
		(book_id, media_type, requested_format, actual_format, status, source_file_count, source_bytes,
		destination_path, destination_name, checksum, error, created_at, completed_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		export.BookID, export.MediaType, export.RequestedFormat, export.ActualFormat, export.Status,
		export.SourceFileCount, export.SourceBytes, export.DestinationPath,
		export.DestinationName, export.Checksum, export.Error, export.CreatedAt,
		nullableTime(export.CompletedAt), export.UpdatedAt)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (d *DB) UpdateAppleBooksExport(export *models.AppleBooksExport) error {
	if export == nil || export.ID == 0 {
		return sql.ErrNoRows
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`UPDATE apple_books_exports SET actual_format = ?, status = ?,
		source_file_count = ?, source_bytes = ?, destination_path = ?, destination_name = ?,
		checksum = ?, error = ?, completed_at = ?, updated_at = ? WHERE id = ?`,
		export.ActualFormat, export.Status, export.SourceFileCount, export.SourceBytes,
		export.DestinationPath, export.DestinationName, export.Checksum, export.Error,
		nullableTime(export.CompletedAt), export.UpdatedAt, export.ID)
	return err
}

func (d *DB) GetAppleBooksExport(id int64) (*models.AppleBooksExport, error) {
	return scanAppleBooksExport(d.db.QueryRow(`SELECT `+appleBooksExportColumns+` FROM apple_books_exports WHERE id = ?`, id))
}

func (d *DB) ListAppleBooksExports(bookID int64, limit int) ([]models.AppleBooksExport, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	query := `SELECT ` + appleBooksExportColumns + ` FROM apple_books_exports`
	args := []any{}
	if bookID > 0 {
		query += ` WHERE book_id = ?`
		args = append(args, bookID)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var exports []models.AppleBooksExport
	for rows.Next() {
		export, err := scanAppleBooksExport(rows)
		if err != nil {
			return nil, err
		}
		exports = append(exports, *export)
	}
	return exports, rows.Err()
}

func scanAppleBooksExport(row interface{ Scan(...any) error }) (*models.AppleBooksExport, error) {
	var export models.AppleBooksExport
	var created, completed, updated string
	if err := row.Scan(&export.ID, &export.BookID, &export.MediaType, &export.RequestedFormat, &export.ActualFormat,
		&export.Status, &export.SourceFileCount, &export.SourceBytes, &export.DestinationPath,
		&export.DestinationName, &export.Checksum, &export.Error, &created, &completed, &updated); err != nil {
		return nil, err
	}
	export.CreatedAt = parseDBTime(created)
	export.UpdatedAt = parseDBTime(updated)
	if completed != "" {
		value := parseDBTime(completed)
		if !value.IsZero() {
			export.CompletedAt = &value
		}
	}
	return &export, nil
}
