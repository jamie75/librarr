package libraryimport

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/JeremiahM37/librarr/internal/library"
	"github.com/JeremiahM37/librarr/internal/organize"
)

type MetadataResolver struct{}

var plannerParenRe = regexp.MustCompile(`\([^)]*\)`)
var plannerBracketRe = regexp.MustCompile(`\[[^\]]*\]`)
var plannerWhitespaceRe = regexp.MustCompile(`\s+`)

func (r *MetadataResolver) Resolve(ctx context.Context, candidate *ImportCandidate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch {
	case candidate.IsDirectory && candidate.MediaType == library.MediaTypeAudiobook:
		r.resolveAudiobookDirectory(candidate)
	case isEbookFormat(candidate.Format):
		r.resolveEbook(candidate)
	default:
		r.resolveFilenameOnly(candidate)
	}
	return nil
}

func (r *MetadataResolver) resolveEbook(candidate *ImportCandidate) {
	embedded := organize.ExtractEmbeddedEbookMetadata(candidate.Path)
	filename := organize.ExtractFilenameEbookMetadata(candidate.Path)
	if strings.EqualFold(candidate.Format, "mobi") && embedded.Title == "" && embedded.Author == "" {
		filename = parsePlannerMOBIFilename(candidate.Path)
	}
	selected := organize.ExtractEbookMetadata(candidate.Path)
	if strings.EqualFold(candidate.Format, "mobi") && embedded.Title == "" && embedded.Author == "" {
		selected = filename
	}
	candidate.Metadata = CandidateMetadata{
		EmbeddedTitle:  strings.TrimSpace(embedded.Title),
		EmbeddedAuthor: strings.TrimSpace(embedded.Author),
		FilenameTitle:  strings.TrimSpace(filename.Title),
		FilenameAuthor: strings.TrimSpace(filename.Author),
		SelectedTitle:  strings.TrimSpace(selected.Title),
		SelectedAuthor: strings.TrimSpace(selected.Author),
	}
	candidate.Evidence = append(candidate.Evidence,
		metadataEvidence("embedded_title", candidate.Metadata.EmbeddedTitle, "embedded_metadata"),
		metadataEvidence("embedded_author", candidate.Metadata.EmbeddedAuthor, "embedded_metadata"),
		metadataEvidence("filename_title", candidate.Metadata.FilenameTitle, "filename_fallback"),
		metadataEvidence("filename_author", candidate.Metadata.FilenameAuthor, "filename_fallback"),
		metadataEvidence("selected_title", candidate.Metadata.SelectedTitle, selectedTitleSource(candidate.Metadata), "selected metadata title for planning"),
		metadataEvidence("selected_author", candidate.Metadata.SelectedAuthor, selectedAuthorSource(candidate.Metadata), "selected metadata author for planning"),
	)
}

func (r *MetadataResolver) resolveFilenameOnly(candidate *ImportCandidate) {
	title := strings.TrimSpace(strings.TrimSuffix(filepath.Base(candidate.Path), filepath.Ext(candidate.Path)))
	title = strings.Join(strings.Fields(title), " ")
	candidate.Metadata = CandidateMetadata{
		FilenameTitle: title,
		SelectedTitle: title,
	}
	candidate.Evidence = append(candidate.Evidence,
		metadataEvidence("filename_title", candidate.Metadata.FilenameTitle, "filename_fallback"),
		metadataEvidence("selected_title", candidate.Metadata.SelectedTitle, "filename_fallback", "selected filename-derived title for planning"),
	)
}

func (r *MetadataResolver) resolveAudiobookDirectory(candidate *ImportCandidate) {
	folderName := strings.TrimSpace(filepath.Base(candidate.Path))
	meta := organize.ExtractAudioMetaFromDir(candidate.Path)
	selectedTitle := folderName
	selectedAuthor := ""
	if meta != nil {
		if strings.TrimSpace(meta.Album) != "" {
			selectedTitle = strings.TrimSpace(meta.Album)
		} else if strings.TrimSpace(meta.Title) != "" {
			selectedTitle = strings.TrimSpace(meta.Title)
		}
		selectedAuthor = strings.TrimSpace(meta.Artist)
	}
	candidate.Metadata = CandidateMetadata{
		FilenameTitle:  folderName,
		FilenameAuthor: selectedAuthor,
		SelectedTitle:  selectedTitle,
		SelectedAuthor: selectedAuthor,
	}
	candidate.Evidence = append(candidate.Evidence,
		metadataEvidence("filename_title", candidate.Metadata.FilenameTitle, "directory_name"),
		metadataEvidence("selected_title", candidate.Metadata.SelectedTitle, "audio_metadata", "selected audiobook title for planning"),
		metadataEvidence("selected_author", candidate.Metadata.SelectedAuthor, "audio_metadata", "selected audiobook author for planning"),
	)
}

func isEbookFormat(format string) bool {
	switch strings.ToLower(format) {
	case "epub", "mobi", "azw3", "pdf":
		return true
	default:
		return false
	}
}

func parsePlannerMOBIFilename(path string) organize.EbookMetadata {
	name := strings.TrimSpace(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	name = plannerBracketRe.ReplaceAllString(name, "")
	name = plannerParenRe.ReplaceAllString(name, "")
	name = plannerWhitespaceRe.ReplaceAllString(name, " ")
	name = strings.TrimSpace(name)
	if name == "" {
		return organize.EbookMetadata{}
	}
	rawParts := strings.Split(name, " - ")
	parts := make([]string, 0, len(rawParts))
	for _, part := range rawParts {
		part = strings.Trim(strings.TrimSpace(part), "- ")
		if part != "" {
			parts = append(parts, part)
		}
	}
	switch len(parts) {
	case 0:
		return organize.EbookMetadata{}
	case 1:
		return organize.EbookMetadata{Title: parts[0]}
	case 2:
		if looksLikePersonName(parts[0]) {
			return organize.EbookMetadata{Title: parts[1], Author: parts[0]}
		}
		if looksLikePersonName(parts[1]) {
			return organize.EbookMetadata{Title: parts[0], Author: parts[1]}
		}
		return organize.EbookMetadata{Title: parts[0], Author: parts[1]}
	default:
		return organize.EbookMetadata{Title: parts[len(parts)-2], Author: parts[len(parts)-1]}
	}
}

func looksLikePersonName(value string) bool {
	words := strings.Fields(value)
	if len(words) < 2 || len(words) > 4 {
		return false
	}
	first := strings.ToLower(strings.Trim(words[0], ".,;:!?\"'"))
	if first == "a" || first == "an" || first == "the" {
		return false
	}
	for _, word := range words {
		word = strings.Trim(word, ".,;:!?\"'")
		if word == "" {
			return false
		}
		runes := []rune(word)
		if len(runes) == 1 {
			if !unicode.IsUpper(runes[0]) {
				return false
			}
			continue
		}
		if strings.HasSuffix(word, ".") && len([]rune(strings.TrimSuffix(word, "."))) == 1 {
			continue
		}
		if !unicode.IsUpper(runes[0]) {
			return false
		}
	}
	return true
}

func metadataEvidence(signal, value, source string, explanation ...string) PlanningEvidence {
	exp := ""
	if len(explanation) > 0 {
		exp = explanation[0]
	}
	return PlanningEvidence{
		Signal:      signal,
		Value:       value,
		Source:      source,
		Confidence:  library.ConfidenceHigh,
		Explanation: exp,
	}
}

func selectedTitleSource(meta CandidateMetadata) string {
	if meta.EmbeddedTitle != "" {
		return "embedded_metadata"
	}
	if meta.FilenameTitle != "" {
		return "filename_fallback"
	}
	return "unknown"
}

func selectedAuthorSource(meta CandidateMetadata) string {
	if meta.EmbeddedAuthor != "" {
		return "embedded_metadata"
	}
	if meta.FilenameAuthor != "" {
		return "filename_fallback"
	}
	return "unknown"
}
