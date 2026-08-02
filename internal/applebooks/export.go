// Package applebooks prepares existing normalized books for import into
// Apple Books. It never changes the source library.
package applebooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jamie75/librarr/internal/db"
	"github.com/jamie75/librarr/internal/library"
	"github.com/jamie75/librarr/internal/models"
	"github.com/jamie75/librarr/internal/safepath"
)

const (
	StatusQueued    = "queued"
	StatusExporting = "exporting"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	maxTracks       = 10000
	maxSourceBytes  = int64(100 * 1024 * 1024 * 1024)
)

type Config struct {
	Enabled       bool
	ExportDir     string
	Overwrite     bool
	EbookRoot     string
	AudiobookRoot string
	CoverRoot     string
}

type Store interface {
	CreateAppleBooksExport(*models.AppleBooksExport) (int64, error)
	UpdateAppleBooksExport(*models.AppleBooksExport) error
	GetAppleBooksExport(int64) (*models.AppleBooksExport, error)
	ListAppleBooksExports(int64, int) ([]models.AppleBooksExport, error)
}

type Exporter struct {
	library *library.LibraryService
	store   Store
	config  Config
}

// DeliveryFile is a validated source file that may be served to an
// authenticated client. The path has already been checked against the
// media-specific configured root; callers must still perform their final
// sink-local validation immediately before opening it.
type DeliveryFile struct {
	FileID      int64
	BookID      int64
	Format      string
	Path        string
	Filename    string
	ContentType string
	Size        int64
	ModTime     time.Time
}

func NewExporter(libraryService *library.LibraryService, store Store, config Config) *Exporter {
	return &Exporter{library: libraryService, store: store, config: config}
}

func (e *Exporter) Config() Config { return e.config }

func (e *Exporter) SetConfig(config Config) {
	if e != nil {
		e.config = config
	}
}

// PrepareDownload selects and validates one supported source file without
// copying or modifying it. A fileID of zero selects the first file in the
// requested format; callers can pass fileID when a book has multiple tracks
// of the same format.
func (e *Exporter) PrepareDownload(ctx context.Context, bookID, fileID int64, requestedFormat string) (*DeliveryFile, error) {
	if e == nil || e.library == nil {
		return nil, errors.New("download delivery is unavailable in this repository mode")
	}
	book, err := e.library.GetBook(ctx, bookID)
	if err != nil {
		return nil, fmt.Errorf("load book: %w", err)
	}
	if book.MediaType != library.MediaTypeEbook && book.MediaType != library.MediaTypeAudiobook {
		return nil, fmt.Errorf("unsupported media type %q for download", book.MediaType)
	}
	files, err := e.library.GetBookFiles(ctx, bookID)
	if err != nil {
		return nil, fmt.Errorf("load book files: %w", err)
	}
	requested := normalizeFormat(requestedFormat)
	if requested == "auto" {
		requested = ""
	}
	type downloadCandidate struct {
		file   library.BookFile
		format string
	}
	var candidates []downloadCandidate
	for _, file := range files {
		if fileID > 0 && file.ID != fileID {
			continue
		}
		format := extension(file.Path)
		if strings.TrimSpace(file.Format) != "" {
			storedFormat := extension("." + file.Format)
			if downloadFormatAllowed(book.MediaType, storedFormat) {
				format = storedFormat
			}
		}
		if !downloadFormatAllowed(book.MediaType, format) || (requested != "" && format != requested) {
			continue
		}
		candidates = append(candidates, downloadCandidate{file: file, format: format})
	}
	if len(candidates) == 0 {
		if fileID > 0 {
			return nil, errors.New("requested file is not available in the requested format")
		}
		if requested != "" {
			return nil, fmt.Errorf("requested format %q is not available for this book", requested)
		}
		return nil, errors.New("no supported downloadable file is available for this book")
	}
	if book.MediaType == library.MediaTypeAudiobook && candidates[0].format == "mp3" && len(candidates) > 1 {
		return nil, errors.New("direct download for multi-track audiobooks is not available yet")
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].format != candidates[j].format && requested == "" && book.MediaType == library.MediaTypeAudiobook {
			return candidates[i].format == "m4b"
		}
		if candidates[i].file.ID != candidates[j].file.ID {
			return candidates[i].file.ID < candidates[j].file.ID
		}
		return candidates[i].file.Path < candidates[j].file.Path
	})
	candidate := candidates[0]
	file := candidate.file
	format := candidate.format
	root := e.config.EbookRoot
	if book.MediaType == library.MediaTypeAudiobook {
		root = e.config.AudiobookRoot
	}
	validated, err := validateSource(root, file.Path)
	if err != nil {
		return nil, fmt.Errorf("unsafe %s source: %w", book.MediaType, err)
	}
	info, err := os.Stat(validated)
	if err != nil {
		return nil, fmt.Errorf("read download source: %w", err)
	}
	if info.IsDir() {
		return nil, errors.New("directory audiobook downloads are not supported; choose an individual MP3 or M4B file")
	}
	author := primaryContributor(book.Contributors, library.RoleAuthor)
	base := strings.TrimSpace(book.Title)
	if base == "" {
		return nil, errors.New("book title is required for download")
	}
	if author = strings.TrimSpace(author); author != "" {
		base = author + " - " + base
	}
	filename := safeName(base) + "." + format
	if filename == "."+format {
		return nil, errors.New("book title is not a valid download filename")
	}
	return &DeliveryFile{
		FileID: file.ID, BookID: bookID, Format: format, Path: validated,
		Filename: filename, ContentType: deliveryMIME(format), Size: info.Size(), ModTime: info.ModTime(),
	}, nil
}

func downloadFormatAllowed(mediaType library.MediaType, format string) bool {
	switch mediaType {
	case library.MediaTypeEbook:
		return format == "epub" || format == "pdf"
	case library.MediaTypeAudiobook:
		return format == "mp3" || format == "m4b"
	default:
		return false
	}
}

func deliveryMIME(format string) string {
	switch format {
	case "epub":
		return "application/epub+zip"
	case "pdf":
		return "application/pdf"
	case "mp3":
		return "audio/mpeg"
	case "m4b":
		return "audio/mp4"
	default:
		return "application/octet-stream"
	}
}

// SafeFilename applies the same filename policy used by Apple Books exports.
func SafeFilename(value string) string { return safeName(value) }

// ValidateSource applies the same configured-root and symlink checks used by
// Apple Books exports to another delivery surface.
func ValidateSource(root, candidate string) (string, error) { return validateSource(root, candidate) }

func (e *Exporter) Export(ctx context.Context, bookID int64, requestedFormat string) (*models.AppleBooksExport, error) {
	if !e.config.Enabled {
		return nil, errors.New("Apple Books export is disabled")
	}
	if e.library == nil || e.store == nil {
		return nil, errors.New("Apple Books export is unavailable in this repository mode")
	}
	root, err := validatedExportRoot(e.config.ExportDir)
	if err != nil {
		return nil, fmt.Errorf("invalid Apple Books export folder: %w", err)
	}
	book, err := e.library.GetBook(ctx, bookID)
	if err != nil {
		return nil, fmt.Errorf("load book: %w", err)
	}
	if book.MediaType == library.MediaTypeEbook {
		return e.exportEbook(ctx, root, book, normalizeFormat(requestedFormat))
	}
	if book.MediaType != library.MediaTypeAudiobook {
		return nil, errors.New("only audiobooks and ebooks can be exported to Apple Books")
	}
	files, err := e.library.GetBookFiles(ctx, bookID)
	if err != nil {
		return nil, fmt.Errorf("load audiobook files: %w", err)
	}
	tracks, err := e.sourceTracks(files)
	if err != nil {
		return nil, err
	}
	if len(tracks) == 0 {
		return nil, errors.New("audiobook has no readable MP3 or M4B files")
	}
	if len(tracks) > maxTracks {
		return nil, fmt.Errorf("audiobook has too many tracks (maximum %d)", maxTracks)
	}
	var sourceBytes int64
	for _, track := range tracks {
		if track.size > maxSourceBytes-sourceBytes {
			return nil, fmt.Errorf("audiobook exceeds the maximum export size")
		}
		sourceBytes += track.size
	}

	requested := normalizeFormat(requestedFormat)
	actual := chooseFormat(requested, tracks)
	if actual == "" {
		return nil, fmt.Errorf("requested format %q is not available", requestedFormat)
	}
	author := primaryContributor(book.Contributors, library.RoleAuthor)
	if author == "" {
		author = "Unknown Author"
	}
	title := strings.TrimSpace(book.Title)
	if title == "" {
		return nil, errors.New("audiobook title is required")
	}
	baseName := safeName(author + " - " + title)
	isSingle := len(tracks) == 1
	destinationName := baseName + "." + actual
	if !isSingle {
		destinationName = baseName
	}
	destination, err := safeDestination(root, destinationName)
	if err != nil {
		return nil, err
	}
	if exists, err := pathExists(destination); err != nil {
		return nil, err
	} else if exists && !e.config.Overwrite {
		return nil, fmt.Errorf("Apple Books export already exists: %s", destinationName)
	} else if exists && isSymlink(destination) {
		return nil, errors.New("Apple Books export destination is a symlink")
	}

	now := time.Now().UTC()
	record := &models.AppleBooksExport{
		BookID: bookID, MediaType: string(library.MediaTypeAudiobook), RequestedFormat: requested, ActualFormat: actual,
		Status: StatusQueued, SourceFileCount: len(tracks), SourceBytes: sourceBytes,
		DestinationPath: destination, DestinationName: destinationName,
		CreatedAt: now, UpdatedAt: now,
	}
	record.ID, err = e.store.CreateAppleBooksExport(record)
	if err != nil {
		return nil, fmt.Errorf("create export record: %w", err)
	}
	record.Status = StatusExporting
	record.UpdatedAt = time.Now().UTC()
	if err := e.store.UpdateAppleBooksExport(record); err != nil {
		return nil, fmt.Errorf("update export status: %w", err)
	}

	if err := e.writeExport(ctx, root, destination, destinationName, book, tracks, actual, sourceBytes); err != nil {
		record.Status = StatusFailed
		record.Error = safeError(err)
		record.UpdatedAt = time.Now().UTC()
		_ = e.store.UpdateAppleBooksExport(record)
		return record, err
	}
	record.Status = StatusCompleted
	record.CompletedAt = timePtr(time.Now().UTC())
	record.UpdatedAt = time.Now().UTC()
	record.Checksum, err = exportChecksum(destination)
	if err != nil {
		record.Status = StatusFailed
		record.Error = safeError(err)
		record.CompletedAt = nil
		record.UpdatedAt = time.Now().UTC()
		_ = e.store.UpdateAppleBooksExport(record)
		return record, err
	}
	if err := e.store.UpdateAppleBooksExport(record); err != nil {
		return record, fmt.Errorf("save completed export: %w", err)
	}
	slog.Info("Apple Books audiobook export completed", "book_id", bookID, "export_id", record.ID, "format", actual, "files", len(tracks))
	return record, nil
}

type sourceEbook struct {
	path string
	ext  string
	size int64
}

func (e *Exporter) exportEbook(ctx context.Context, root string, book *library.Book, requestedFormat string) (*models.AppleBooksExport, error) {
	files, err := e.library.GetBookFiles(ctx, book.ID)
	if err != nil {
		return nil, fmt.Errorf("load ebook files: %w", err)
	}
	source, requested, err := e.sourceEbook(files, requestedFormat)
	if err != nil {
		return nil, err
	}
	if requested == "" {
		return nil, errors.New("ebook has no readable EPUB or PDF files")
	}
	if source.size > maxSourceBytes {
		return nil, errors.New("ebook exceeds the maximum export size")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	author := primaryContributor(book.Contributors, library.RoleAuthor)
	title := strings.TrimSpace(book.Title)
	if title == "" {
		return nil, errors.New("ebook title is required")
	}
	baseName := title
	if author = strings.TrimSpace(author); author != "" {
		baseName = author + " - " + title
	}
	destinationName := safeName(baseName) + "." + source.ext
	destination, err := safeDestination(root, destinationName)
	if err != nil {
		return nil, err
	}
	if exists, err := pathExists(destination); err != nil {
		return nil, err
	} else if exists && !e.config.Overwrite {
		return nil, fmt.Errorf("Apple Books export already exists: %s", destinationName)
	} else if exists && isSymlink(destination) {
		return nil, errors.New("Apple Books export destination is a symlink")
	}

	now := time.Now().UTC()
	record := &models.AppleBooksExport{
		BookID: book.ID, MediaType: string(library.MediaTypeEbook), RequestedFormat: requestedFormat,
		ActualFormat: source.ext, Status: StatusQueued, SourceFileCount: 1, SourceBytes: source.size,
		DestinationPath: destination, DestinationName: destinationName, CreatedAt: now, UpdatedAt: now,
	}
	record.ID, err = e.store.CreateAppleBooksExport(record)
	if err != nil {
		return nil, fmt.Errorf("create export record: %w", err)
	}
	record.Status = StatusExporting
	record.UpdatedAt = time.Now().UTC()
	if err := e.store.UpdateAppleBooksExport(record); err != nil {
		return nil, fmt.Errorf("update export status: %w", err)
	}
	if err := e.writeSingleExport(ctx, root, destination, destinationName, e.config.EbookRoot, source.path); err != nil {
		record.Status = StatusFailed
		record.Error = safeError(err)
		record.UpdatedAt = time.Now().UTC()
		_ = e.store.UpdateAppleBooksExport(record)
		return record, err
	}
	record.Status = StatusCompleted
	record.CompletedAt = timePtr(time.Now().UTC())
	record.UpdatedAt = time.Now().UTC()
	record.Checksum, err = exportChecksum(destination)
	if err != nil {
		record.Status = StatusFailed
		record.Error = safeError(err)
		record.CompletedAt = nil
		record.UpdatedAt = time.Now().UTC()
		_ = e.store.UpdateAppleBooksExport(record)
		return record, err
	}
	if err := e.store.UpdateAppleBooksExport(record); err != nil {
		return record, fmt.Errorf("save completed export: %w", err)
	}
	slog.Info("Apple Books ebook export completed", "book_id", book.ID, "export_id", record.ID, "format", source.ext)
	return record, nil
}

func (e *Exporter) sourceEbook(files []library.BookFile, requested string) (sourceEbook, string, error) {
	requested = normalizeFormat(requested)
	if requested != "auto" && requested != "epub" && requested != "pdf" {
		return sourceEbook{}, "", fmt.Errorf("requested format %q is not supported for ebooks; choose auto, epub, or pdf", requested)
	}
	var candidates []sourceEbook
	var unsupported []string
	for _, file := range files {
		if file.MediaType != "" && file.MediaType != library.MediaTypeEbook {
			continue
		}
		ext := extension(file.Path)
		if strings.TrimSpace(file.Format) != "" {
			ext = extension("." + file.Format)
		}
		if ext != "epub" && ext != "pdf" {
			if ext != "" {
				unsupported = append(unsupported, ext)
			}
			continue
		}
		if requested != "auto" && ext != requested {
			continue
		}
		validated, err := validateSource(e.config.EbookRoot, file.Path)
		if err != nil {
			return sourceEbook{}, "", fmt.Errorf("unsafe ebook source: %w", err)
		}
		info, err := os.Stat(validated)
		if err != nil {
			return sourceEbook{}, "", fmt.Errorf("read ebook source: %w", err)
		}
		if info.IsDir() {
			return sourceEbook{}, "", fmt.Errorf("ebook source %q is a directory; only EPUB and PDF files are supported", filepath.Base(validated))
		}
		candidates = append(candidates, sourceEbook{path: validated, ext: ext, size: info.Size()})
	}
	if len(candidates) == 0 {
		if len(unsupported) > 0 {
			return sourceEbook{}, "", fmt.Errorf("ebook format %q is not supported by Apple Books export; only EPUB and PDF are supported", unsupported[0])
		}
		if requested != "auto" {
			return sourceEbook{}, "", fmt.Errorf("requested format %q is not available for this ebook", requested)
		}
		return sourceEbook{}, "", errors.New("ebook has no readable EPUB or PDF files")
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].ext != candidates[j].ext {
			return candidates[i].ext == "epub"
		}
		return candidates[i].path < candidates[j].path
	})
	return candidates[0], candidates[0].ext, nil
}

type sourceTrack struct {
	path string
	name string
	ext  string
	size int64
	file library.BookFile
}

func (e *Exporter) sourceTracks(files []library.BookFile) ([]sourceTrack, error) {
	seen := map[string]bool{}
	var tracks []sourceTrack
	for _, file := range files {
		if file.MediaType != "" && file.MediaType != library.MediaTypeAudiobook {
			continue
		}
		path := strings.TrimSpace(file.Path)
		if path == "" {
			continue
		}
		validated, err := validateSource(e.config.AudiobookRoot, path)
		if err != nil {
			return nil, fmt.Errorf("unsafe audiobook source: %w", err)
		}
		info, err := os.Stat(validated)
		if err != nil {
			return nil, fmt.Errorf("read audiobook source: %w", err)
		}
		if info.IsDir() {
			found, err := discoverTracks(e.config.AudiobookRoot, validated)
			if err != nil {
				return nil, err
			}
			for _, track := range found {
				if !seen[track.path] {
					seen[track.path] = true
					tracks = append(tracks, track)
				}
			}
			continue
		}
		if !supportedAudioExtension(validated) {
			continue
		}
		if !seen[validated] {
			seen[validated] = true
			tracks = append(tracks, sourceTrack{path: validated, name: filepath.Base(validated), ext: extension(validated), size: info.Size(), file: file})
		}
	}
	sort.SliceStable(tracks, func(i, j int) bool { return trackOrder(tracks[i]) < trackOrder(tracks[j]) })
	return tracks, nil
}

func discoverTracks(root, directory string) ([]sourceTrack, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read audiobook directory: %w", err)
	}
	var tracks []sourceTrack
	for _, entry := range entries {
		candidate := filepath.Join(directory, entry.Name())
		validated, err := validateSource(root, candidate)
		if err != nil {
			return nil, fmt.Errorf("unsafe audiobook track: %w", err)
		}
		if entry.IsDir() {
			nested, err := discoverTracks(root, validated)
			if err != nil {
				return nil, err
			}
			tracks = append(tracks, nested...)
			continue
		}
		if !supportedAudioExtension(validated) {
			continue
		}
		info, err := os.Stat(validated)
		if err != nil {
			return nil, err
		}
		tracks = append(tracks, sourceTrack{path: validated, name: filepath.Base(validated), ext: extension(validated), size: info.Size()})
	}
	sort.SliceStable(tracks, func(i, j int) bool { return trackOrder(tracks[i]) < trackOrder(tracks[j]) })
	return tracks, nil
}

func (e *Exporter) writeExport(ctx context.Context, root, destination, destinationName string, book *library.Book, tracks []sourceTrack, actual string, sourceBytes int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.config.Overwrite {
		if err := removeExistingDestination(root, destination); err != nil {
			return err
		}
	}
	temp, err := os.MkdirTemp(root, ".librarr-apple-books-")
	if err != nil {
		return fmt.Errorf("create temporary export: %w", err)
	}
	defer os.RemoveAll(temp)
	if len(tracks) == 1 {
		tempFile := filepath.Join(temp, destinationName)
		if err := copyValidatedFile(e.config.AudiobookRoot, tracks[0].path, temp, tempFile); err != nil {
			return err
		}
	} else {
		packageDir := filepath.Join(temp, destinationName)
		validatedPackageDir, err := safepath.UnderRoot(temp, packageDir)
		if err != nil {
			return fmt.Errorf("unsafe Apple Books package: %w", err)
		}
		if err := os.MkdirAll(validatedPackageDir, 0755); err != nil {
			return fmt.Errorf("create Apple Books package: %w", err)
		}
		for index, track := range tracks {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := fmt.Sprintf("%03d - %s", index+1, safeName(strings.TrimSuffix(track.name, filepath.Ext(track.name)))) + filepath.Ext(track.name)
			if err := copyValidatedFile(e.config.AudiobookRoot, track.path, validatedPackageDir, filepath.Join(validatedPackageDir, name)); err != nil {
				return err
			}
		}
		if err := e.writeCover(ctx, book.ID, validatedPackageDir); err != nil {
			return err
		}
		manifest := map[string]any{"title": book.Title, "author": primaryContributor(book.Contributors, library.RoleAuthor), "tracks": len(tracks), "format": actual, "source_bytes": sourceBytes}
		data, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return err
		}
		manifestPath := filepath.Join(validatedPackageDir, "manifest.json")
		validatedManifest, err := safepath.UnderRoot(validatedPackageDir, manifestPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(validatedManifest, append(data, '\n'), 0644); err != nil {
			return fmt.Errorf("write Apple Books manifest: %w", err)
		}
	}
	staged := filepath.Join(temp, filepath.Base(destination))
	validatedStaged, err := safepath.ExistingUnderRoot(temp, staged)
	if err != nil {
		return err
	}
	if _, err := os.Stat(validatedStaged); err != nil {
		return fmt.Errorf("staged export missing: %w", err)
	}
	if err := os.Rename(validatedStaged, destination); err != nil {
		return fmt.Errorf("publish Apple Books export: %w", err)
	}
	return nil
}

func (e *Exporter) writeSingleExport(ctx context.Context, root, destination, destinationName, sourceRoot, sourcePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.config.Overwrite {
		if err := removeExistingDestination(root, destination); err != nil {
			return err
		}
	}
	temp, err := os.MkdirTemp(root, ".librarr-apple-books-")
	if err != nil {
		return fmt.Errorf("create temporary export: %w", err)
	}
	defer os.RemoveAll(temp)
	tempFile := filepath.Join(temp, destinationName)
	if err := copyValidatedFile(sourceRoot, sourcePath, temp, tempFile); err != nil {
		return err
	}
	validatedStaged, err := safepath.ExistingUnderRoot(temp, tempFile)
	if err != nil {
		return err
	}
	if err := os.Rename(validatedStaged, destination); err != nil {
		return fmt.Errorf("publish Apple Books export: %w", err)
	}
	return nil
}

func (e *Exporter) writeCover(ctx context.Context, bookID int64, packageDir string) error {
	if strings.TrimSpace(e.config.CoverRoot) == "" {
		return nil
	}
	cover, err := e.library.GetPrimaryCover(ctx, bookID)
	if err != nil || cover == nil || strings.TrimSpace(cover.LocalPath) == "" {
		return nil
	}
	source, err := validateSource(e.config.CoverRoot, cover.LocalPath)
	if err != nil {
		return fmt.Errorf("unsafe audiobook cover: %w", err)
	}
	ext := filepath.Ext(source)
	if ext == "" {
		ext = ".jpg"
	}
	return copyValidatedFile(e.config.CoverRoot, source, packageDir, filepath.Join(packageDir, "cover"+ext))
}

func copyValidatedFile(sourceRoot, source, destinationRoot, destination string) error {
	validatedSource, err := validateSource(sourceRoot, source)
	if err != nil {
		return err
	}
	validatedDestination, err := safepath.UnderRoot(destinationRoot, destination)
	if err != nil {
		return fmt.Errorf("unsafe Apple Books output: %w", err)
	}
	input, err := os.Open(validatedSource)
	if err != nil {
		return fmt.Errorf("open Apple Books source: %w", err)
	}
	defer input.Close()
	output, err := os.OpenFile(validatedDestination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("create Apple Books output: %w", err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		_ = os.Remove(validatedDestination)
		return fmt.Errorf("copy Apple Books source: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close Apple Books output: %w", err)
	}
	return nil
}

func validateSource(root, candidate string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("audiobook source root is not configured")
	}
	return safepath.ExistingUnderRoot(root, candidate)
}

func validatedExportRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) {
		return "", errors.New("export folder must be an absolute path")
	}
	cleaned := filepath.Clean(root)
	validated, err := safepath.UnderRoot(filepath.Dir(cleaned), cleaned)
	if err != nil {
		return "", fmt.Errorf("unsafe export folder: %w", err)
	}
	if err := os.MkdirAll(validated, 0755); err != nil {
		return "", fmt.Errorf("create export folder: %w", err)
	}
	return safepath.ExistingUnderRoot(validated, validated)
}

func safeDestination(root, name string) (string, error) {
	name = safeName(name)
	if name == "" || name == "." || name == ".." {
		return "", errors.New("invalid Apple Books export name")
	}
	destination, err := safepath.UnderRoot(root, filepath.Join(root, name))
	if err != nil {
		return "", fmt.Errorf("unsafe Apple Books destination: %w", err)
	}
	return destination, nil
}

func removeExistingDestination(root, destination string) error {
	validated, err := safepath.UnderRoot(root, destination)
	if err != nil {
		return err
	}
	info, err := os.Lstat(validated)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to overwrite a symlink destination")
	}
	if err := os.RemoveAll(validated); err != nil {
		return fmt.Errorf("remove previous Apple Books export: %w", err)
	}
	return nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func isSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

func exportChecksum(path string) (string, error) {
	path = filepath.Clean(path)
	validatedPath, err := safepath.ExistingUnderRoot(filepath.Dir(path), path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(validatedPath)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		file, err := os.Open(validatedPath)
		if err != nil {
			return "", err
		}
		defer file.Close()
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			return "", err
		}
		return hex.EncodeToString(hash.Sum(nil)), nil
	}
	hash := sha256.New()
	var files []string
	if err := filepath.WalkDir(validatedPath, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			files = append(files, current)
		}
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(files)
	for _, current := range files {
		validated, err := safepath.ExistingUnderRoot(validatedPath, current)
		if err != nil {
			return "", err
		}
		file, err := os.Open(validated)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(hash, file); err != nil {
			file.Close()
			return "", err
		}
		file.Close()
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func supportedAudioExtension(path string) bool {
	ext := extension(path)
	return ext == "mp3" || ext == "m4b"
}

func extension(path string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
}

func normalizeFormat(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "auto"
	}
	return value
}

func chooseFormat(requested string, tracks []sourceTrack) string {
	if requested == "m4b" {
		if len(tracks) == 1 && tracks[0].ext == "m4b" {
			return "m4b"
		}
		return ""
	}
	if requested == "mp3" {
		for _, track := range tracks {
			if track.ext != "mp3" {
				return ""
			}
		}
		return "mp3"
	}
	if len(tracks) == 1 {
		return tracks[0].ext
	}
	for _, track := range tracks {
		if track.ext != "mp3" {
			return ""
		}
	}
	return "mp3-package"
}

func trackOrder(track sourceTrack) string {
	if value := strings.TrimSpace(track.file.EmbeddedMetadata["disc_number"]); value != "" {
		if disc, err := strconv.Atoi(strings.Split(value, "/")[0]); err == nil {
			trackNumber := 0
			if raw := strings.TrimSpace(track.file.EmbeddedMetadata["track_number"]); raw != "" {
				trackNumber, _ = strconv.Atoi(strings.Split(raw, "/")[0])
			}
			return fmt.Sprintf("%08d-%08d-%s", disc, trackNumber, strings.ToLower(track.path))
		}
	}
	return strings.ToLower(track.path)
}

func primaryContributor(contributors []library.Contributor, role library.ContributorRole) string {
	for _, contributor := range contributors {
		if contributor.Name == "" {
			continue
		}
		if role == "" || len(contributor.Roles) == 0 {
			return contributor.Name
		}
		for _, contributorRole := range contributor.Roles {
			if contributorRole == role {
				return contributor.Name
			}
		}
	}
	return ""
}

func safeName(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, char := range value {
		switch {
		case char == '/' || char == '\\' || char == ':' || char == 0 || char < 32:
			builder.WriteRune('-')
		default:
			builder.WriteRune(char)
		}
	}
	value = strings.Join(strings.Fields(builder.String()), " ")
	value = strings.Trim(value, " .")
	if len([]rune(value)) > 180 {
		value = string([]rune(value)[:180])
	}
	return value
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.NewReplacer("\r", " ", "\n", " ", "\x00", " ").Replace(err.Error())
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}

func timePtr(value time.Time) *time.Time { return &value }

var _ Store = (*db.DB)(nil)
