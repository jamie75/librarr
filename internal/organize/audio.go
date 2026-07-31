package organize

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// AudioMeta holds extracted audio metadata.
type AudioMeta struct {
	Artist          string
	Album           string
	Title           string
	Narrator        string
	Composer        string
	Year            string
	Genre           string
	Comment         string
	TrackNumber     int
	DiscNumber      int
	DurationSeconds int64
	ChapterCount    int
	Abridged        bool
	Cover           *ExtractedCover
	Embedded        bool
}

// AudioTrack is the bounded metadata summary for one physical audiobook file.
// The path is retained so the importer can attach every track to one edition.
type AudioTrack struct {
	Path            string
	Format          string
	Title           string
	Artist          string
	Album           string
	Narrator        string
	TrackNumber     int
	DiscNumber      int
	DurationSeconds int64
	Cover           *ExtractedCover
	Embedded        bool
}

// AudiobookMeta is an aggregate over one audiobook file or a directory of
// tracks. It deliberately keeps only catalog metadata; audio bytes remain in
// their original files.
type AudiobookMeta struct {
	Title           string
	Author          string
	Narrator        string
	DurationSeconds int64
	TrackCount      int
	ChapterCount    int
	Abridged        bool
	Format          string
	Cover           *ExtractedCover
	Tracks          []AudioTrack
	Consistent      bool
	Embedded        bool
}

// ExtractAudioMeta extracts metadata from an audio file.
// It tries to read ID3v2 tags from MP3 files by parsing the header directly.
// Falls back to filename parsing if no tags found.
func ExtractAudioMeta(path string) *AudioMeta {
	// Try to read ID3v2 tags from the file header.
	if embedded := extractEmbeddedAudioMeta(path); embedded != nil {
		return embedded
	}

	// Fallback: parse from filename.
	meta := parseAudioFilename(path)
	if strings.HasSuffix(strings.ToLower(path), ".mp3") {
		meta.DurationSeconds = estimateMP3Duration(path)
	}
	return meta
}

func extractEmbeddedAudioMeta(path string) *AudioMeta {
	if strings.HasSuffix(strings.ToLower(path), ".mp3") {
		return readID3v2Tags(path)
	}
	if strings.HasSuffix(strings.ToLower(path), ".m4b") {
		return readM4BMetadata(path)
	}
	return nil
}

// readID3v2Tags reads basic ID3v2.3/2.4 tags from an MP3 file.
func readID3v2Tags(path string) *AudioMeta {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	// Check for ID3v2 header.
	header := make([]byte, 10)
	if _, err := f.Read(header); err != nil {
		return nil
	}

	if string(header[:3]) != "ID3" {
		return nil
	}

	// Parse header size (syncsafe integer).
	size := int(header[6])<<21 | int(header[7])<<14 | int(header[8])<<7 | int(header[9])
	if size <= 0 || size > 1024*1024 { // limit to 1MB header
		return nil
	}

	tagData := make([]byte, size)
	if _, err := f.Read(tagData); err != nil {
		return nil
	}

	meta := &AudioMeta{}
	pos := 0
	for pos+10 < len(tagData) {
		frameID := string(tagData[pos : pos+4])
		if frameID[0] == 0 {
			break
		}

		frameSize := int(tagData[pos+4])<<24 | int(tagData[pos+5])<<16 | int(tagData[pos+6])<<8 | int(tagData[pos+7])
		if frameSize <= 0 || pos+10+frameSize > len(tagData) {
			break
		}

		frameData := tagData[pos+10 : pos+10+frameSize]

		// Skip encoding byte.
		text := ""
		if len(frameData) > 1 {
			encoding := frameData[0]
			switch encoding {
			case 0, 3: // ISO-8859-1 or UTF-8
				text = strings.TrimRight(string(frameData[1:]), "\x00")
			case 1, 2: // UTF-16
				// Simple extraction: skip BOM and null bytes.
				var b []byte
				for i := 1; i < len(frameData); i++ {
					if frameData[i] != 0 {
						b = append(b, frameData[i])
					}
				}
				text = string(b)
			}
		}

		text = strings.TrimSpace(text)

		switch frameID {
		case "TPE1": // Artist
			if meta.Artist == "" {
				meta.Artist = text
			}
		case "TALB": // Album
			if meta.Album == "" {
				meta.Album = text
			}
		case "TIT2": // Title
			if meta.Title == "" {
				meta.Title = text
			}
		case "TPE2": // Album artist
			if meta.Artist == "" {
				meta.Artist = text
			}
		case "TCOM":
			meta.Composer = text
		case "TDRC", "TYER":
			meta.Year = text
		case "TCON":
			meta.Genre = text
		case "COMM":
			meta.Comment = text
		case "TRCK":
			meta.TrackNumber = parseTrackNumber(text)
		case "TPOS":
			meta.DiscNumber = parseTrackNumber(text)
		case "APIC":
			if cover := parseID3Cover(frameData); cover != nil {
				meta.Cover = cover
			}
		}

		pos += 10 + frameSize
	}

	if meta.Artist != "" || meta.Album != "" || meta.Title != "" || meta.Cover != nil {
		meta.Embedded = true
		meta.DurationSeconds = estimateMP3Duration(path)
		return meta
	}
	return nil
}

func estimateMP3Duration(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= 0 {
		return 0
	}
	// A bounded MPEG frame-header scan gives useful duration for normal CBR
	// audiobook tracks without requiring an audio decoder.
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	buf := make([]byte, 4)
	for offset := int64(0); offset+4 <= info.Size() && offset < 1<<20; offset++ {
		if _, err := f.ReadAt(buf, offset); err != nil {
			break
		}
		if buf[0] != 0xff || buf[1]&0xe0 != 0xe0 {
			continue
		}
		version := (buf[1] >> 3) & 3
		layer := (buf[1] >> 1) & 3
		bitrateIndex := (buf[2] >> 4) & 15
		sampleIndex := (buf[2] >> 2) & 3
		if layer != 1 || bitrateIndex == 0 || bitrateIndex == 15 || sampleIndex == 3 {
			continue
		}
		bitrates := []int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
		samples := []int{44100, 48000, 32000}
		if int(bitrateIndex) >= len(bitrates) {
			continue
		}
		bitrate := int64(bitrates[bitrateIndex]) * 1000
		sampleRate := int64(samples[sampleIndex])
		if version == 2 {
			sampleRate /= 2
		} else if version == 0 {
			sampleRate /= 2
		}
		if bitrate > 0 && sampleRate > 0 {
			return info.Size() * 8 / bitrate
		}
	}
	return 0
}

func parseTrackNumber(value string) int {
	value = strings.TrimSpace(strings.SplitN(value, "/", 2)[0])
	var n int
	_, _ = fmt.Sscanf(value, "%d", &n)
	return n
}

func parseID3Cover(data []byte) *ExtractedCover {
	if len(data) < 5 {
		return nil
	}
	pos := 1
	for pos < len(data) && data[pos] != 0 {
		pos++
	}
	if pos+2 >= len(data) {
		return nil
	}
	pos++
	pos++ // picture type
	for pos < len(data) && data[pos] != 0 {
		pos++
	}
	if pos >= len(data) {
		return nil
	}
	pos++
	if pos >= len(data) {
		return nil
	}
	if pos >= len(data) {
		return nil
	}
	imageData := append([]byte(nil), data[pos:]...)
	mimeType := "image/jpeg"
	if strings.HasPrefix(string(imageData), "\x89PNG") {
		mimeType = "image/png"
	}
	return &ExtractedCover{Data: imageData, MimeType: mimeType, Ext: extensionForMime(mimeType), Source: "embedded_audiobook"}
}

// readM4BMetadata reads common iTunes/MP4 metadata atoms without decoding
// audio. It is intentionally bounded; unsupported atoms are ignored and the
// filename/path fallback remains available.
func readM4BMetadata(path string) *AudioMeta {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > 32<<20 {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, 32<<20))
	if err != nil {
		return nil
	}
	meta := &AudioMeta{}
	parseMP4Atoms(data, meta)
	if meta.Title == "" && meta.Album == "" && meta.Artist == "" && meta.Cover == nil && meta.DurationSeconds == 0 && meta.ChapterCount == 0 {
		return nil
	}
	meta.Embedded = true
	return meta
}

func parseMP4Atoms(data []byte, meta *AudioMeta) {
	for pos := 0; pos+8 <= len(data); {
		size := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		name := string(data[pos+4 : pos+8])
		if size == 1 || size < 8 || pos+size > len(data) {
			break
		}
		body := data[pos+8 : pos+size]
		nameTitle := string([]byte{0xa9, 'n', 'a', 'm'})
		nameAlbum := string([]byte{0xa9, 'a', 'l', 'b'})
		nameArtist := string([]byte{0xa9, 'A', 'R', 'T'})
		nameDate := string([]byte{0xa9, 'd', 'a', 'y'})
		switch name {
		case nameTitle, nameAlbum, nameArtist, "aART", nameDate, "desc":
			if len(body) >= 16 && string(body[4:8]) == "data" {
				value := strings.TrimSpace(string(body[16:]))
				switch name {
				case nameTitle:
					meta.Title = firstString(meta.Title, value)
				case nameAlbum:
					meta.Album = firstString(meta.Album, value)
				case nameArtist, "aART":
					meta.Artist = firstString(meta.Artist, value)
				case nameDate:
					meta.Year = firstString(meta.Year, value)
				case "desc":
					meta.Comment = firstString(meta.Comment, value)
				}
			}
		case "covr":
			if len(body) >= 16 && string(body[4:8]) == "data" {
				imageData := append([]byte(nil), body[16:]...)
				mimeType, ext := "image/jpeg", ".jpg"
				if strings.HasPrefix(string(imageData), "\x89PNG") {
					mimeType, ext = "image/png", ".png"
				}
				meta.Cover = &ExtractedCover{Data: imageData, MimeType: mimeType, Ext: ext, Source: "embedded_audiobook"}
			}
		case "mvhd":
			if len(body) >= 20 {
				version := body[0]
				if version == 0 {
					timescale := binary.BigEndian.Uint32(body[12:16])
					duration := binary.BigEndian.Uint32(body[16:20])
					if timescale > 0 {
						meta.DurationSeconds = int64(duration) / int64(timescale)
					}
				} else if len(body) >= 32 {
					timescale := binary.BigEndian.Uint32(body[20:24])
					duration := binary.BigEndian.Uint64(body[24:32])
					if timescale > 0 {
						meta.DurationSeconds = int64(duration / uint64(timescale))
					}
				}
			}
		case "chpl":
			if len(body) >= 9 {
				meta.ChapterCount = int(body[8])
			}
		}
		if name == "moov" || name == "udta" || name == "ilst" || name == "trak" || name == "mdia" || name == "minf" || name == "stbl" {
			parseMP4Atoms(body, meta)
		} else if name == "meta" && len(body) >= 4 {
			parseMP4Atoms(body[4:], meta)
		}
		pos += size
	}
}

func firstString(current, value string) string {
	if strings.TrimSpace(current) != "" {
		return current
	}
	return strings.TrimSpace(value)
}

// parseAudioFilename extracts artist and title from filename patterns.
func parseAudioFilename(path string) *AudioMeta {
	name := filepath.Base(path)
	ext := filepath.Ext(name)
	name = strings.TrimSuffix(name, ext)

	meta := &AudioMeta{}

	// Common pattern: "Artist - Title" or "Artist - Album - Title"
	dashRe := regexp.MustCompile(`^(.+?)\s*-\s*(.+)$`)
	if m := dashRe.FindStringSubmatch(name); len(m) >= 3 {
		meta.Artist = strings.TrimSpace(m[1])
		remainder := strings.TrimSpace(m[2])

		// Check for second dash (Artist - Album - Title).
		if m2 := dashRe.FindStringSubmatch(remainder); len(m2) >= 3 {
			meta.Album = strings.TrimSpace(m2[1])
			meta.Title = strings.TrimSpace(m2[2])
		} else {
			meta.Title = remainder
		}
		return meta
	}

	meta.Title = name
	return meta
}

// ExtractAudiobookPathMetadata derives conservative audiobook metadata from a
// library path when embedded tags are unavailable. It assumes the immediate
// parent folder is the author and the filename stem is the title, which matches
// the mounted-library layout used by Librarr's explicit library scanner.
func ExtractAudiobookPathMetadata(path string) *AudioMeta {
	author := strings.TrimSpace(filepath.Base(filepath.Dir(path)))
	filenameMeta := parseAudiobookFilename(path)
	if isGenericAudioParent(author) && filenameMeta.Artist != "" {
		author = filenameMeta.Artist
	} else if isGenericAudioParent(author) {
		author = ""
	}
	title := audiobookTitleFromFile(path, author)
	if isGenericAudioParent(filepath.Base(filepath.Dir(path))) && filenameMeta.Title != "" {
		title = filenameMeta.Title
	}
	return &AudioMeta{Artist: author, Title: title}
}

func isGenericAudioParent(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return normalized == "" || normalized == "incoming" || normalized == "downloads" || normalized == "audiobook" || normalized == "audiobooks" || normalized == "audio" || regexp.MustCompile(`^\d+$`).MatchString(normalized)
}

func parseAudiobookFilename(path string) *AudioMeta {
	name := strings.TrimSpace(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	name = regexp.MustCompile(`(?i)\s*\[[^]]*(?:mp3|m4b|m4a|kbps|eng|english)[^]]*\]\s*$`).ReplaceAllString(name, "")
	name = regexp.MustCompile(`(?i)\s*\((?:audiobook|unabridged|abridged|retail)[^)]*\)\s*[eE]?$`).ReplaceAllString(name, "")
	name = strings.TrimSpace(name)
	meta := &AudioMeta{}
	if match := regexp.MustCompile(`(?i)^(.+?)\s+by\s+(.+)$`).FindStringSubmatch(name); len(match) == 3 {
		meta.Title, meta.Artist = strings.TrimSpace(match[1]), strings.TrimSpace(match[2])
		return meta
	}
	parts := strings.Split(name, " - ")
	if len(parts) >= 3 && strings.EqualFold(strings.TrimSpace(parts[len(parts)-1]), "unabridged") {
		parts = parts[:len(parts)-1]
	}
	if len(parts) >= 2 && looksLikeAudioPerson(parts[1]) {
		meta.Title, meta.Artist = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		return meta
	}
	meta.Title = name
	return meta
}

func looksLikeAudioPerson(value string) bool {
	words := strings.Fields(strings.TrimSpace(value))
	return len(words) >= 2 && len(words) <= 5
}

func audiobookTitleFromFile(path, author string) string {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if author != "" {
		prefix := strings.TrimSpace(author) + " - "
		if strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
			name = strings.TrimSpace(name[len(prefix):])
		}
	}
	name = stripAudiobookPartSuffix(name)
	return strings.TrimSpace(name)
}

func stripAudiobookPartSuffix(title string) string {
	parts := strings.Split(title, " - ")
	if len(parts) <= 1 {
		return title
	}
	last := strings.TrimSpace(parts[len(parts)-1])
	if strings.EqualFold(last, "part") || strings.HasPrefix(strings.ToLower(last), "part ") {
		return strings.TrimSpace(strings.Join(parts[:len(parts)-1], " - "))
	}
	return title
}

// ExtractAudioMetaFromDir scans a directory for audio files and extracts
// artist/album from the first valid file found.
func ExtractAudioMetaFromDir(dirPath string) *AudioMeta {
	audioExts := map[string]bool{
		".mp3": true, ".m4a": true, ".m4b": true,
		".ogg": true, ".flac": true, ".opus": true,
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if !audioExts[ext] {
			continue
		}
		meta := ExtractAudioMeta(filepath.Join(dirPath, entry.Name()))
		if meta != nil && (meta.Artist != "" || meta.Album != "") {
			return meta
		}
	}
	return nil
}

// ExtractAudiobookMetadata aggregates supported tracks recursively. A title is
// accepted only when album tags agree; otherwise callers can send the item to
// manual review instead of silently merging unrelated audio files.
func ExtractAudiobookMetadata(path string) *AudiobookMeta {
	paths := []string{}
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if info.IsDir() {
		_ = filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
			if walkErr == nil && !entry.IsDir() && isAudiobookExtension(filepath.Ext(current)) {
				paths = append(paths, current)
			}
			return nil
		})
	} else if isAudiobookExtension(filepath.Ext(path)) {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil
	}
	result := &AudiobookMeta{Tracks: make([]AudioTrack, 0, len(paths)), Consistent: true}
	var title, author string
	for _, trackPath := range paths {
		meta := ExtractAudioMeta(trackPath)
		track := AudioTrack{Path: trackPath, Format: strings.TrimPrefix(strings.ToLower(filepath.Ext(trackPath)), ".")}
		if meta != nil {
			track.Title, track.Artist, track.Album, track.Narrator = meta.Title, meta.Artist, meta.Album, firstString(meta.Narrator, meta.Artist)
			track.TrackNumber, track.DiscNumber, track.DurationSeconds, track.Cover = meta.TrackNumber, meta.DiscNumber, meta.DurationSeconds, meta.Cover
			track.Embedded = meta.Embedded
			if result.Cover == nil {
				result.Cover = meta.Cover
			}
		}
		if track.Embedded {
			result.Embedded = true
		}
		result.Tracks = append(result.Tracks, track)
		result.DurationSeconds += track.DurationSeconds
		if track.DurationSeconds > 0 || track.Path != "" {
			result.TrackCount++
		}
		candidateTitle := firstString(track.Album, track.Title)
		if candidateTitle != "" {
			if title == "" {
				title = candidateTitle
			} else if !strings.EqualFold(title, candidateTitle) {
				result.Consistent = false
			}
		}
		if track.Artist != "" {
			if author == "" {
				author = track.Artist
			} else if !strings.EqualFold(author, track.Artist) {
				result.Consistent = false
			}
		}
	}
	result.Title, result.Author, result.Narrator = title, author, author
	if len(result.Tracks) == 1 {
		result.Format = result.Tracks[0].Format
	} else if allAudioFormat(result.Tracks, "mp3") {
		result.Format = "mp3"
	} else if allAudioFormat(result.Tracks, "m4b") {
		result.Format = "m4b"
	} else {
		result.Format = "audio"
	}
	return result
}

func isAudiobookExtension(ext string) bool {
	ext = strings.ToLower(ext)
	return ext == ".mp3" || ext == ".m4b"
}
func allAudioFormat(tracks []AudioTrack, format string) bool {
	for _, track := range tracks {
		if track.Format != format {
			return false
		}
	}
	return len(tracks) > 0
}
