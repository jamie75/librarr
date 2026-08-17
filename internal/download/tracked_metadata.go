package download

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jamie75/librarr/internal/library"
	libraryimport "github.com/jamie75/librarr/internal/library/import"
	"github.com/jamie75/librarr/internal/models"
	"github.com/jamie75/librarr/internal/organize"
)

// trackedImportMetadata is the metadata chosen before organization creates a
// destination path. A Wanted record is an explicit user-selected identity, so
// it must take precedence over release-name inference and embedded metadata.
type trackedImportMetadata struct {
	override libraryimport.CandidateMetadata
	source   string
}

func (w *Watcher) canonicalTrackedImportMetadata(item models.TrackedDownload) trackedImportMetadata {
	if w == nil || w.db == nil || !strings.EqualFold(strings.TrimSpace(item.Source), "wanted") {
		return trackedImportMetadata{}
	}
	const wantedPrefix = "wanted:"
	sourceID := strings.TrimSpace(item.SourceID)
	if !strings.HasPrefix(strings.ToLower(sourceID), wantedPrefix) {
		return trackedImportMetadata{}
	}
	wantedID, err := strconv.ParseInt(strings.TrimSpace(sourceID[len(wantedPrefix):]), 10, 64)
	if err != nil || wantedID <= 0 {
		return trackedImportMetadata{}
	}
	wanted, err := w.db.GetWantedBook(wantedID)
	if err != nil || wanted == nil || !sameTrackedMediaType(item.MediaType, wanted.MediaType) {
		return trackedImportMetadata{}
	}
	return trackedImportMetadata{
		source: "library_record",
		override: libraryimport.CandidateMetadata{
			SelectedTitle:  strings.TrimSpace(wanted.Title),
			SelectedAuthor: strings.TrimSpace(wanted.Author),
			Series:         strings.TrimSpace(wanted.Series),
			Publisher:      strings.TrimSpace(wanted.Publisher),
			Language:       strings.TrimSpace(wanted.Language),
			ISBN:           strings.TrimSpace(wanted.ISBN),
			Description:    strings.TrimSpace(wanted.Description),
		},
	}
}

func sameTrackedMediaType(tracked, wanted string) bool {
	tracked = strings.TrimSpace(strings.ToLower(tracked))
	wanted = strings.TrimSpace(strings.ToLower(wanted))
	return tracked != "" && wanted != "" && library.MediaType(tracked) == library.MediaType(wanted)
}

func chooseTrackedAudiobookMetadata(torrentName string, embedded *libraryimport.CandidateMetadata, canonical trackedImportMetadata) (title, author, source string) {
	if embedded != nil {
		title = strings.TrimSpace(embedded.SelectedTitle)
		author = strings.TrimSpace(embedded.SelectedAuthor)
		if title != "" || author != "" {
			source = "embedded_audio_metadata"
		}
	}
	if canonical.source != "" {
		title = firstNonEmpty(canonical.override.SelectedTitle, title)
		author = firstNonEmpty(canonical.override.SelectedAuthor, author)
		source = canonical.source
	}
	if inferredTitle, inferredAuthor := inferAudiobookReleaseMetadata(torrentName); title == "" || author == "" {
		title = firstNonEmpty(title, inferredTitle)
		author = firstNonEmpty(author, inferredAuthor)
		if source == "" && (inferredTitle != "" || inferredAuthor != "") {
			source = "release_name"
		}
	}
	title = firstNonEmpty(title, torrentName)
	author = firstNonEmpty(author, "Unknown")
	if source == "" {
		source = "torrent_name"
	}
	return title, author, source
}

func inferAudiobookReleaseMetadata(name string) (title, author string) {
	name = strings.TrimSpace(name)
	if ext := strings.ToLower(filepath.Ext(name)); isAudiobookMetadataExtension(ext) {
		name = strings.TrimSpace(strings.TrimSuffix(name, ext))
	}
	if bracket := strings.Index(name, "["); bracket > 0 {
		name = strings.TrimSpace(name[:bracket])
	}
	if before, after, found := strings.Cut(name, " by "); found {
		return strings.TrimSpace(before), strings.TrimSpace(after)
	}
	// Preserve the legacy convention only as a final inference fallback.
	if before, after, found := strings.Cut(name, " - "); found {
		return strings.TrimSpace(after), strings.TrimSpace(before)
	}
	return name, ""
}

func isAudiobookMetadataExtension(ext string) bool {
	switch ext {
	case ".m4b", ".mp3", ".m4a", ".aac", ".flac", ".ogg", ".opus":
		return true
	default:
		return false
	}
}

func ebookMetadataSource(metadata organize.EbookMetadata) string {
	if strings.TrimSpace(metadata.Title) != "" || strings.TrimSpace(metadata.Author) != "" {
		return "embedded_ebook_metadata"
	}
	return "filename_fallback"
}
