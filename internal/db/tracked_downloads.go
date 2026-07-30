package db

import (
	"database/sql"
	"time"

	"github.com/jamie75/librarr/internal/models"
)

const trackedDownloadColumns = `id, client_id, client_type, download_id, info_hash, title,
	media_type, source, source_id, category, remote_save_path, status,
	last_observed_status, progress, remote_path, local_path, import_status,
	last_error, created_at, completed_at, imported_at`

func (d *DB) SaveTrackedDownload(item *models.TrackedDownload) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`INSERT OR REPLACE INTO tracked_downloads (`+trackedDownloadColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.ClientID, item.ClientType, item.DownloadID, item.InfoHash,
		item.Title, item.MediaType, item.Source, item.SourceID, item.Category,
		item.RemoteSavePath, item.Status, item.LastObservedStatus, item.Progress,
		item.RemotePath, item.LocalPath, item.ImportStatus, item.LastError,
		item.CreatedAt.Unix(), nullableTime(item.CompletedAt), nullableTime(item.ImportedAt))
	return err
}

func (d *DB) UpdateTrackedDownload(item *models.TrackedDownload) error {
	return d.SaveTrackedDownload(item)
}

func (d *DB) GetTrackedDownloads() ([]models.TrackedDownload, error) {
	rows, err := d.db.Query(`SELECT ` + trackedDownloadColumns + ` FROM tracked_downloads ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []models.TrackedDownload
	for rows.Next() {
		item, err := scanTrackedDownload(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (d *DB) GetTrackedDownload(id string) (*models.TrackedDownload, error) {
	row := d.db.QueryRow(`SELECT `+trackedDownloadColumns+` FROM tracked_downloads WHERE id = ?`, id)
	return scanTrackedDownload(row)
}

func (d *DB) FindTrackedDownloadByHash(clientID, infoHash string) (*models.TrackedDownload, error) {
	row := d.db.QueryRow(`SELECT `+trackedDownloadColumns+` FROM tracked_downloads WHERE client_id = ? AND info_hash = ? LIMIT 1`, clientID, infoHash)
	return scanTrackedDownload(row)
}

func scanTrackedDownload(row interface{ Scan(...any) error }) (*models.TrackedDownload, error) {
	var item models.TrackedDownload
	var created, completed, imported sql.NullFloat64
	if err := row.Scan(&item.ID, &item.ClientID, &item.ClientType, &item.DownloadID,
		&item.InfoHash, &item.Title, &item.MediaType, &item.Source, &item.SourceID,
		&item.Category, &item.RemoteSavePath, &item.Status, &item.LastObservedStatus,
		&item.Progress, &item.RemotePath, &item.LocalPath, &item.ImportStatus,
		&item.LastError, &created, &completed, &imported); err != nil {
		return nil, err
	}
	item.CreatedAt = time.Unix(int64(created.Float64), 0)
	if completed.Valid {
		value := time.Unix(int64(completed.Float64), 0)
		item.CompletedAt = &value
	}
	if imported.Valid {
		value := time.Unix(int64(imported.Float64), 0)
		item.ImportedAt = &value
	}
	return &item, nil
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.Unix()
}
