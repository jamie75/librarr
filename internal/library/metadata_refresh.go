package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/jamie75/librarr/internal/organize"
)

// MetadataRefreshConflict records a manual value that was intentionally kept
// while a newer file value was discovered.
type MetadataRefreshConflict struct {
	BookID        int64  `json:"book_id"`
	Field         string `json:"field"`
	ExistingValue string `json:"existing_value"`
	NewValue      string `json:"new_value"`
	Source        string `json:"source"`
	Confidence    string `json:"confidence"`
}

type MetadataRefreshResult struct {
	BookID        int64                     `json:"book_id"`
	Refreshed     bool                      `json:"refreshed"`
	Updated       bool                      `json:"updated"`
	ChangedFields []string                  `json:"changed_fields,omitempty"`
	CoverUpdated  bool                      `json:"cover_updated"`
	ManualReview  bool                      `json:"manual_review"`
	Conflicts     []MetadataRefreshConflict `json:"conflicts,omitempty"`
	Format        string                    `json:"format,omitempty"`
	Error         string                    `json:"error,omitempty"`
}

type MetadataRefreshReport struct {
	BooksRefreshed int                     `json:"books_refreshed"`
	BooksUpdated   int                     `json:"books_updated"`
	CoversUpdated  int                     `json:"covers_updated"`
	ManualReview   int                     `json:"manual_review"`
	Results        []MetadataRefreshResult `json:"results"`
}

// RefreshBookMetadata rereads an existing normalized file and updates only
// catalog metadata. It never creates a book, edition, contributor, or file.
func (s *LibraryService) RefreshBookMetadata(ctx context.Context, bookID int64) (MetadataRefreshResult, error) {
	var result MetadataRefreshResult
	err := s.WithinTransaction(ctx, func(txCtx context.Context) error {
		var refreshErr error
		result, refreshErr = s.refreshBookMetadata(txCtx, bookID)
		return refreshErr
	})
	return result, err
}

func (s *LibraryService) refreshBookMetadata(ctx context.Context, bookID int64) (MetadataRefreshResult, error) {
	result := MetadataRefreshResult{BookID: bookID}
	book, err := s.GetBook(ctx, bookID)
	if err != nil {
		return result, err
	}
	files, err := s.GetBookFiles(ctx, bookID)
	if err != nil {
		return result, err
	}
	metadata, err := s.GetBookMetadata(ctx, bookID)
	if err != nil {
		return result, err
	}
	editions, err := s.ListBookEditions(ctx, bookID)
	if err != nil {
		return result, err
	}
	if len(files) == 0 || len(editions) == 0 {
		return result, fmt.Errorf("book has no refreshable file")
	}

	for _, file := range files {
		path := strings.TrimSpace(file.Path)
		if path == "" {
			continue
		}
		if !isRefreshablePath(file) {
			continue
		}
		validatedPath, pathErr := s.validateStoredFilePath(path)
		if pathErr != nil {
			result.Error = pathErr.Error()
			continue
		}
		path = validatedPath
		if err := ctx.Err(); err != nil {
			return result, err
		}

		var fields map[MetadataField]string
		fileMetadata := cloneMetadata(file.EmbeddedMetadata)
		format := strings.ToLower(strings.TrimSpace(file.Format))
		source := "embedded_metadata"
		confidence := ConfidenceHigh
		if file.MediaType == MediaTypeAudiobook || isAudiobookPath(path) {
			audio := refreshAudiobookMetadata(path, files)
			if audio == nil {
				continue
			}
			if audio.Format != "" {
				format = audio.Format
			} else if len(audio.Tracks) > 0 {
				format = audio.Tracks[0].Format
			}
			fields = map[MetadataField]string{}
			if strings.TrimSpace(audio.Title) != "" {
				fileMetadata["title"] = audio.Title
			}
			if strings.TrimSpace(audio.Author) != "" {
				fileMetadata["author"] = audio.Author
			}
			if strings.TrimSpace(audio.Narrator) != "" {
				fileMetadata["narrator"] = audio.Narrator
			}
			fileMetadata["duration_seconds"] = fmt.Sprintf("%d", audio.DurationSeconds)
			fileMetadata["track_count"] = fmt.Sprintf("%d", audio.TrackCount)
			fileMetadata["chapter_count"] = fmt.Sprintf("%d", audio.ChapterCount)
			fileMetadata["abridged"] = fmt.Sprintf("%t", audio.Abridged)
			fileMetadata["metadata_source"] = map[bool]string{true: "embedded_audiobook_metadata", false: "filename_parsing"}[audio.Embedded]
			if audio.Embedded {
				source = "embedded_audiobook_metadata"
			} else {
				source = "filename_parsing"
				confidence = ConfidenceMedium
			}
			if audio.Title != "" {
				fields[MetadataFieldTitle] = audio.Title
			}
			trackPath := path
			if len(audio.Tracks) > 0 {
				trackPath = audio.Tracks[0].Path
			}
			trackMeta := organize.ExtractAudioMeta(trackPath)
			if trackMeta != nil {
				if strings.TrimSpace(trackMeta.Year) != "" {
					fields[MetadataFieldPublicationDate] = trackMeta.Year
				}
				if strings.TrimSpace(trackMeta.Comment) != "" {
					fields[MetadataFieldDescription] = trackMeta.Comment
				}
			}
			if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
				if err := s.reconcileAudiobookTracks(ctx, file, audio); err != nil {
					return result, err
				}
				result.addChangedField("files")
			}
		} else if strings.EqualFold(file.Format, "epub") || strings.EqualFold(filepath.Ext(path), ".epub") {
			ebook, extractErr := organize.ReadEPUBPackageMetadata(path)
			if extractErr != nil || ebook == nil {
				continue
			}
			fields = map[MetadataField]string{}
			if ebook.Title != "" {
				fields[MetadataFieldTitle] = ebook.Title
				fileMetadata["title"] = ebook.Title
			}
			if ebook.Author != "" {
				fileMetadata["author"] = ebook.Author
			}
			fileMetadata["metadata_source"] = "embedded_epub_metadata"
			source = "embedded_epub_metadata"
		} else {
			continue
		}

		if len(fields) > 0 {
			before := book.Title
			if entry, ok := metadata.Fields[MetadataFieldTitle]; ok && entry.ManualOverride && fields[MetadataFieldTitle] != entry.Value {
				result.ManualReview = true
				result.Conflicts = append(result.Conflicts, MetadataRefreshConflict{BookID: bookID, Field: string(MetadataFieldTitle), ExistingValue: entry.Value, NewValue: fields[MetadataFieldTitle], Source: source, Confidence: string(confidence)})
				delete(fields, MetadataFieldTitle)
			}
			if len(fields) > 0 {
				if _, err := s.ApplyBookMetadataSource(ctx, MetadataUpdate{BookID: bookID, Source: source, Confidence: confidence, Fields: fields, UpdatedAt: time.Now().UTC()}); err != nil {
					return result, err
				}
			}
			if fields[MetadataFieldTitle] != "" && !strings.EqualFold(before, fields[MetadataFieldTitle]) {
				result.Updated = true
			}
		}
		if file.MediaType == MediaTypeAudiobook || isAudiobookPath(path) {
			audio := refreshAudiobookMetadata(path, files)
			if audio != nil {
				if err := s.refreshAudioContributors(ctx, editions[0].ID, audio); err != nil {
					return result, err
				}
			}
		}
		persistedFile, err := s.UpdateFileMetadata(ctx, file.ID, format, fileMetadata)
		if err != nil {
			return result, err
		}
		if !strings.EqualFold(file.Format, persistedFile.Format) {
			result.addChangedField("format")
		}
		if file.EmbeddedMetadata["title"] != persistedFile.EmbeddedMetadata["title"] {
			result.addChangedField("title")
		}
		for _, field := range []string{"author", "narrator", "duration_seconds", "track_count", "chapter_count", "abridged", "metadata_source"} {
			if file.EmbeddedMetadata[field] != persistedFile.EmbeddedMetadata[field] {
				result.addChangedField(field)
			}
		}
		if !reflect.DeepEqual(file.EmbeddedMetadata, persistedFile.EmbeddedMetadata) {
			result.Updated = true
		}
		result.Refreshed = true
		result.Format = format
	}
	if !result.Refreshed {
		return result, fmt.Errorf("no supported metadata found in existing files")
	}
	if refreshedBook, err := s.GetBook(ctx, bookID); err == nil {
		if refreshedBook.Title != book.Title {
			result.addChangedField("title")
		}
		if refreshedBook.Description != book.Description {
			result.addChangedField("description")
		}
		if refreshedBook.Language != book.Language {
			result.addChangedField("language")
		}
	}
	return result, nil
}

func refreshAudiobookMetadata(path string, files []BookFile) *organize.AudiobookMeta {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return organize.ExtractAudiobookMetadata(path)
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		if file.MediaType == MediaTypeAudiobook && isAudiobookPath(file.Path) {
			paths = append(paths, file.Path)
		}
	}
	if len(paths) > 1 {
		return organize.ExtractAudiobookMetadataFromFiles(paths)
	}
	return organize.ExtractAudiobookMetadata(path)
}

func (s *LibraryService) reconcileAudiobookTracks(ctx context.Context, directoryFile BookFile, audio *organize.AudiobookMeta) error {
	if audio == nil || len(audio.Tracks) == 0 {
		return nil
	}
	firstTrack := audio.Tracks[0].Path
	if strings.TrimSpace(firstTrack) == "" {
		return nil
	}
	validatedFirstTrack, err := s.validateStoredFilePath(firstTrack)
	if err != nil {
		return err
	}
	if _, err := s.FindFileByPath(ctx, validatedFirstTrack); err != nil {
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		if _, err := s.MoveFile(ctx, directoryFile.ID, validatedFirstTrack); err != nil {
			return err
		}
	}
	for _, track := range audio.Tracks[1:] {
		trackPath := strings.TrimSpace(track.Path)
		if trackPath == "" {
			continue
		}
		validatedTrackPath, pathErr := s.validateStoredFilePath(trackPath)
		if pathErr != nil {
			continue
		}
		cleanCandidate := filepath.Clean(validatedTrackPath)
		resolvedCandidate, candidateErr := filepath.EvalSymlinks(cleanCandidate)
		safeCandidate := false
		for _, configuredRoot := range s.allowedRoots {
			cleanRoot := filepath.Clean(configuredRoot)
			resolvedRoot, rootErr := filepath.EvalSymlinks(cleanRoot)
			rel, relErr := filepath.Rel(resolvedRoot, resolvedCandidate)
			if rootErr == nil && candidateErr == nil && relErr == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !strings.ContainsAny(cleanCandidate, "\x00\r\n") {
				safeCandidate = true
				break
			}
		}
		if len(s.allowedRoots) == 0 {
			approvedRoot, rootErr := filepath.EvalSymlinks(filepath.Clean(filepath.Dir(trackPath)))
			rel, relErr := filepath.Rel(approvedRoot, resolvedCandidate)
			safeCandidate = rootErr == nil && candidateErr == nil && relErr == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !strings.ContainsAny(cleanCandidate, "\x00\r\n")
		}
		if !safeCandidate {
			continue
		}
		validatedTrackPath = resolvedCandidate
		if _, err := s.FindFileByPath(ctx, validatedTrackPath); err == nil {
			continue
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		info, err := os.Stat(validatedTrackPath)
		if err != nil {
			continue
		}
		attached, err := s.AttachFile(ctx, BookFile{
			EditionID:    directoryFile.EditionID,
			MediaType:    MediaTypeAudiobook,
			Format:       strings.TrimPrefix(strings.ToLower(filepath.Ext(trackPath)), "."),
			Path:         validatedTrackPath,
			OriginalPath: validatedTrackPath,
			Size:         info.Size(),
			Managed:      directoryFile.Managed,
			ImportedAt:   directoryFile.ImportedAt,
			SourceType:   "metadata_refresh",
		})
		if err != nil {
			return err
		}
		if _, err := s.UpdateFileMetadata(ctx, attached.ID, strings.TrimPrefix(strings.ToLower(filepath.Ext(trackPath)), "."), audiobookSummaryMetadata(audio)); err != nil {
			return err
		}
	}
	return nil
}

func audiobookSummaryMetadata(audio *organize.AudiobookMeta) map[string]string {
	metadata := map[string]string{
		"title":            audio.Title,
		"author":           audio.Author,
		"narrator":         audio.Narrator,
		"duration_seconds": fmt.Sprintf("%d", audio.DurationSeconds),
		"track_count":      fmt.Sprintf("%d", audio.TrackCount),
		"chapter_count":    fmt.Sprintf("%d", audio.ChapterCount),
		"abridged":         fmt.Sprintf("%t", audio.Abridged),
		"metadata_source":  map[bool]string{true: "embedded_audiobook_metadata", false: "filename_parsing"}[audio.Embedded],
	}
	return metadata
}

func (r *MetadataRefreshResult) addChangedField(field string) {
	for _, existing := range r.ChangedFields {
		if existing == field {
			return
		}
	}
	r.ChangedFields = append(r.ChangedFields, field)
	r.Updated = true
}

func (s *LibraryService) refreshAudioContributors(ctx context.Context, editionID int64, audio *organize.AudiobookMeta) error {
	contributors, err := s.GetEditionContributors(ctx, editionID)
	if err != nil {
		return err
	}
	hasAuthor, hasNarrator := false, false
	for _, contributor := range contributors {
		for _, role := range contributor.Roles {
			hasAuthor = hasAuthor || role == RoleAuthor
			hasNarrator = hasNarrator || role == RoleNarrator
		}
	}
	if !hasAuthor && strings.TrimSpace(audio.Author) != "" {
		merged, err := s.MergeContributor(ctx, Contributor{Name: audio.Author, SortName: NormalizeKey(audio.Author), Roles: []ContributorRole{RoleAuthor}})
		if err != nil {
			return err
		}
		if err := s.AttachContributor(ctx, editionID, *merged); err != nil {
			return err
		}
	}
	if !hasNarrator && strings.TrimSpace(audio.Narrator) != "" {
		merged, err := s.MergeContributor(ctx, Contributor{Name: audio.Narrator, SortName: NormalizeKey(audio.Narrator), Roles: []ContributorRole{RoleNarrator}})
		if err != nil {
			return err
		}
		if err := s.AttachContributor(ctx, editionID, *merged); err != nil {
			return err
		}
	}
	return nil
}

func (s *LibraryService) RefreshAllMetadata(ctx context.Context) (MetadataRefreshReport, error) {
	books, err := s.ListBooks(ctx, ListBooksQuery{Limit: 100000})
	if err != nil {
		return MetadataRefreshReport{}, err
	}
	report := MetadataRefreshReport{Results: make([]MetadataRefreshResult, 0, len(books))}
	for _, book := range books {
		result, refreshErr := s.RefreshBookMetadata(ctx, book.ID)
		if refreshErr != nil {
			result.Error = refreshErr.Error()
		} else {
			report.BooksRefreshed++
			if result.Updated {
				report.BooksUpdated++
			}
			if result.ManualReview {
				report.ManualReview++
			}
		}
		report.Results = append(report.Results, result)
	}
	return report, nil
}

func cloneMetadata(input map[string]string) map[string]string {
	output := make(map[string]string, len(input)+8)
	for key, value := range input {
		output[key] = value
	}
	return output
}

func isAudiobookPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".mp3" || ext == ".m4b" || ext == ".m4a" || ext == ".flac" || ext == ".ogg" || ext == ".opus" || (ext == "" && strings.TrimSpace(path) != "")
}

func isRefreshablePath(file BookFile) bool {
	return file.MediaType == MediaTypeAudiobook || strings.EqualFold(file.Format, "epub") || strings.EqualFold(filepath.Ext(file.Path), ".epub") || isAudiobookPath(file.Path)
}
