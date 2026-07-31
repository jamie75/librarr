package organize

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/jamie75/librarr/internal/safepath"
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
	logFile := sanitizeAudioLogValue(filepath.Base(path))
	logFile = strings.ReplaceAll(logFile, "\r", "")
	logFile = strings.ReplaceAll(logFile, "\n", "")
	slog.Debug("audio metadata fallback", "file", logFile, "reason", "no supported embedded metadata", "title_found", meta != nil && meta.Title != "", "artist_found", meta != nil && meta.Artist != "", "abridged", meta != nil && meta.Abridged)
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

const maxID3TagBytes = 16 << 20

// readID3v2Tags reads bounded ID3v2.2, 2.3, and 2.4 tags from an MP3 file.
func readID3v2Tags(path string) *AudioMeta {
	validatedPath, err := validateAudioInput(path)
	if err != nil {
		return nil
	}
	approvedRoot, rootErr := filepath.EvalSymlinks(filepath.Clean(filepath.Dir(path)))
	cleanCandidate := filepath.Clean(validatedPath)
	resolvedCandidate, candidateErr := filepath.EvalSymlinks(cleanCandidate)
	rel, relErr := filepath.Rel(approvedRoot, resolvedCandidate)
	if rootErr != nil || candidateErr != nil || relErr != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.ContainsAny(cleanCandidate, "\x00\r\n") {
		return nil
	}
	validatedPath = resolvedCandidate
	f, err := os.Open(validatedPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	// Check for ID3v2 header.
	header := make([]byte, 10)
	if _, err := io.ReadFull(f, header); err != nil {
		return nil
	}

	if string(header[:3]) != "ID3" {
		return nil
	}

	version := header[3]
	if version < 2 || version > 4 {
		return nil
	}
	size, ok := decodeSyncSafe(header[6:10])
	if !ok || size <= 0 || size > maxID3TagBytes {
		logFile := sanitizeAudioLogValue(filepath.Base(validatedPath))
		logFile = strings.ReplaceAll(logFile, "\r", "")
		logFile = strings.ReplaceAll(logFile, "\n", "")
		slog.Debug("mp3 metadata fallback", "file", logFile, "reason", "id3 tag is missing, invalid, or oversized", "id3_version", version)
		return nil
	}

	tagData := make([]byte, size)
	if _, err := io.ReadFull(f, tagData); err != nil {
		return nil
	}
	flags := header[5]
	if flags&0x80 != 0 {
		tagData = removeUnsynchronization(tagData)
	}
	if flags&0x40 != 0 {
		skip, valid := id3ExtendedHeaderLength(version, tagData)
		if !valid || skip > len(tagData) {
			logFile := sanitizeAudioLogValue(filepath.Base(validatedPath))
			logFile = strings.ReplaceAll(logFile, "\r", "")
			logFile = strings.ReplaceAll(logFile, "\n", "")
			slog.Debug("mp3 metadata fallback", "file", logFile, "reason", "invalid id3 extended header", "id3_version", version)
			return nil
		}
		tagData = tagData[skip:]
	}

	meta := &AudioMeta{}
	frameIDs := make([]string, 0, 16)
	var selectedCoverRank = 0
	pos := 0
	for {
		frameID, frameSize, frameHeaderSize, frameFlags, ok := nextID3Frame(tagData[pos:], version)
		if !ok {
			break
		}
		if frameSize <= 0 || frameSize > len(tagData)-pos-frameHeaderSize {
			break
		}
		frameData := append([]byte(nil), tagData[pos+frameHeaderSize:pos+frameHeaderSize+frameSize]...)
		if version >= 3 && frameFlags&0x0002 != 0 {
			frameData = removeUnsynchronization(frameData)
		}
		if version == 4 && frameFlags&0x0001 != 0 && len(frameData) >= 4 {
			frameData = frameData[4:]
		}
		if version == 3 && frameFlags&0x00c0 != 0 || version == 4 && frameFlags&0x000c != 0 {
			pos += frameHeaderSize + frameSize
			continue
		}
		frameIDs = append(frameIDs, frameID)

		switch frameID {
		case "TPE1", "TP1":
			meta.Artist = firstString(meta.Artist, decodeID3TextFrame(frameData))
		case "TALB", "TAL":
			meta.Album = firstString(meta.Album, decodeID3TextFrame(frameData))
		case "TIT2", "TT2":
			meta.Title = firstString(meta.Title, decodeID3TextFrame(frameData))
		case "TPE2", "TP2":
			meta.Artist = firstString(meta.Artist, decodeID3TextFrame(frameData))
		case "TCOM", "TCM":
			meta.Composer = decodeID3TextFrame(frameData)
		case "TDRC", "TYER", "TYE":
			meta.Year = firstString(meta.Year, decodeID3TextFrame(frameData))
		case "TCON", "TCO":
			meta.Genre = decodeID3TextFrame(frameData)
		case "COMM", "COM":
			meta.Comment = decodeID3Comment(frameData)
		case "TRCK", "TRK":
			meta.TrackNumber = parseTrackNumber(decodeID3TextFrame(frameData))
		case "TPOS", "TPA":
			meta.DiscNumber = parseTrackNumber(decodeID3TextFrame(frameData))
		case "APIC":
			if cover, rank := parseID3APIC(frameData); cover != nil && rank > selectedCoverRank {
				meta.Cover, selectedCoverRank = cover, rank
			}
		case "PIC":
			if cover, rank := parseID3PIC(frameData); cover != nil && rank > selectedCoverRank {
				meta.Cover, selectedCoverRank = cover, rank
			}
		}
		pos += frameHeaderSize + frameSize
	}

	logFile := sanitizeAudioLogValue(filepath.Base(validatedPath))
	logFile = strings.ReplaceAll(logFile, "\r", "")
	logFile = strings.ReplaceAll(logFile, "\n", "")
	logFrames := sanitizeAudioLogValues(frameIDs)
	logMime := sanitizeAudioLogValue(coverMime(meta.Cover))
	logMime = strings.ReplaceAll(logMime, "\r", "")
	logMime = strings.ReplaceAll(logMime, "\n", "")
	slog.Debug("mp3 metadata extraction", "file", logFile, "id3_version", version, "frame_ids", logFrames, "title_found", meta.Title != "", "artist_found", meta.Artist != "", "cover_found", meta.Cover != nil, "cover_mime", logMime, "cover_bytes", coverBytes(meta.Cover))
	if meta.Artist != "" || meta.Album != "" || meta.Title != "" || meta.Cover != nil || meta.Comment != "" || meta.Year != "" {
		meta.Embedded = true
		meta.DurationSeconds = estimateMP3Duration(path)
		return meta
	}
	logFile = strings.ReplaceAll(logFile, "\r", "")
	logFile = strings.ReplaceAll(logFile, "\n", "")
	slog.Debug("mp3 metadata fallback", "file", logFile, "reason", "no supported id3 metadata found", "id3_version", version)
	return nil
}

func estimateMP3Duration(path string) int64 {
	validatedPath, err := validateAudioInput(path)
	if err != nil {
		return 0
	}
	approvedRoot, rootErr := filepath.EvalSymlinks(filepath.Clean(filepath.Dir(path)))
	cleanCandidate := filepath.Clean(validatedPath)
	resolvedCandidate, candidateErr := filepath.EvalSymlinks(cleanCandidate)
	rel, relErr := filepath.Rel(approvedRoot, resolvedCandidate)
	if rootErr != nil || candidateErr != nil || relErr != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.ContainsAny(cleanCandidate, "\x00\r\n") {
		return 0
	}
	validatedPath = resolvedCandidate
	info, err := os.Stat(validatedPath)
	if err != nil || info.Size() <= 0 {
		return 0
	}
	// A bounded MPEG frame-header scan gives useful duration for normal CBR
	// audiobook tracks without requiring an audio decoder.
	f, err := os.Open(validatedPath)
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

func decodeSyncSafe(data []byte) (int, bool) {
	if len(data) < 4 || data[0]&0x80 != 0 || data[1]&0x80 != 0 || data[2]&0x80 != 0 || data[3]&0x80 != 0 {
		return 0, false
	}
	return int(data[0])<<21 | int(data[1])<<14 | int(data[2])<<7 | int(data[3]), true
}

func id3ExtendedHeaderLength(version byte, data []byte) (int, bool) {
	if len(data) < 4 {
		return 0, false
	}
	var size int
	var ok bool
	if version == 4 {
		size, ok = decodeSyncSafe(data[:4])
	} else {
		size = int(binary.BigEndian.Uint32(data[:4]))
		ok = true
	}
	if !ok || size < 0 || size > len(data)-4 {
		return 0, false
	}
	return size + 4, true
}

func nextID3Frame(data []byte, version byte) (string, int, int, uint16, bool) {
	headerSize := 10
	idSize := 4
	if version == 2 {
		headerSize = 6
		idSize = 3
	}
	if len(data) < headerSize || data[0] == 0 {
		return "", 0, 0, 0, false
	}
	id := string(data[:idSize])
	for _, r := range id {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return "", 0, 0, 0, false
		}
	}
	var size int
	if version == 2 {
		size = int(data[3])<<16 | int(data[4])<<8 | int(data[5])
	} else if version == 4 {
		var ok bool
		size, ok = decodeSyncSafe(data[4:8])
		if !ok {
			return "", 0, 0, 0, false
		}
	} else {
		size = int(binary.BigEndian.Uint32(data[4:8]))
	}
	var flags uint16
	if version >= 3 {
		flags = binary.BigEndian.Uint16(data[8:10])
	}
	return id, size, headerSize, flags, true
}

func removeUnsynchronization(data []byte) []byte {
	if !bytes.Contains(data, []byte{0xff, 0x00}) {
		return data
	}
	result := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		result = append(result, data[i])
		if data[i] == 0xff && i+1 < len(data) && data[i+1] == 0x00 {
			i++
		}
	}
	return result
}

func decodeID3TextFrame(data []byte) string {
	if len(data) < 2 {
		return ""
	}
	return cleanID3Text(decodeID3String(data[0], data[1:]))
}

func decodeID3Comment(data []byte) string {
	if len(data) < 5 {
		return ""
	}
	encoding := data[0]
	pos := 4
	if encoding == 1 || encoding == 2 {
		pos = skipID3Terminated(data, pos, true)
	} else {
		pos = skipID3Terminated(data, pos, false)
	}
	if pos > len(data) {
		return ""
	}
	return cleanID3Text(decodeID3String(encoding, data[pos:]))
}

func decodeID3String(encoding byte, data []byte) string {
	switch encoding {
	case 0:
		data = bytes.TrimRight(data, "\x00")
		runes := make([]rune, len(data))
		for i, value := range data {
			runes[i] = rune(value)
		}
		return string(runes)
	case 1:
		data = trimUTF16Terminator(data)
		if len(data) < 2 {
			return ""
		}
		littleEndian := data[0] != 0xfe || data[1] != 0xff
		if data[0] == 0xff && data[1] == 0xfe {
			littleEndian = true
		} else if data[0] == 0xfe && data[1] == 0xff {
			littleEndian = false
		}
		data = data[2:]
		return decodeUTF16(data, littleEndian)
	case 2:
		data = trimUTF16Terminator(data)
		if len(data) >= 2 && data[0] == 0xfe && data[1] == 0xff {
			data = data[2:]
		}
		return decodeUTF16(data, false)
	case 3:
		data = bytes.TrimRight(data, "\x00")
		if !utf8.Valid(data) {
			return string(bytes.ToValidUTF8(data, []byte("�")))
		}
		return string(data)
	default:
		return ""
	}
}

func trimUTF16Terminator(data []byte) []byte {
	if len(data) >= 2 && data[len(data)-1] == 0 && data[len(data)-2] == 0 {
		return data[:len(data)-2]
	}
	return data
}

func decodeUTF16(data []byte, littleEndian bool) string {
	if len(data)%2 != 0 {
		data = data[:len(data)-1]
	}
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		if littleEndian {
			units = append(units, binary.LittleEndian.Uint16(data[i:i+2]))
		} else {
			units = append(units, binary.BigEndian.Uint16(data[i:i+2]))
		}
	}
	return string(utf16.Decode(units))
}

func skipID3Terminated(data []byte, pos int, utf16Encoded bool) int {
	if utf16Encoded {
		for pos+1 < len(data) {
			if data[pos] == 0 && data[pos+1] == 0 {
				return pos + 2
			}
			pos += 2
		}
		return len(data)
	}
	for pos < len(data) && data[pos] != 0 {
		pos++
	}
	if pos < len(data) {
		pos++
	}
	return pos
}

func cleanID3Text(value string) string {
	return strings.TrimSpace(strings.TrimRight(value, "\x00"))
}

func parseID3APIC(data []byte) (*ExtractedCover, int) {
	if len(data) < 4 {
		return nil, 0
	}
	encoding := data[0]
	pos := 1
	mimeEnd := bytes.IndexByte(data[pos:], 0)
	if mimeEnd < 0 {
		return nil, 0
	}
	mimeType := strings.ToLower(strings.TrimSpace(string(data[pos : pos+mimeEnd])))
	pos += mimeEnd + 1
	if pos >= len(data) {
		return nil, 0
	}
	pictureType := data[pos]
	pos++
	pos = skipID3Terminated(data, pos, encoding == 1 || encoding == 2)
	return makeID3Cover(data[pos:], mimeType, pictureType)
}

func parseID3PIC(data []byte) (*ExtractedCover, int) {
	if len(data) < 6 {
		return nil, 0
	}
	encoding := data[0]
	format := strings.ToLower(strings.TrimSpace(string(data[1:4])))
	pos := 4
	if pos >= len(data) {
		return nil, 0
	}
	pictureType := data[pos]
	pos++
	pos = skipID3Terminated(data, pos, encoding == 1 || encoding == 2)
	mimeType := "image/" + format
	if format == "jpg" {
		mimeType = "image/jpeg"
	}
	return makeID3Cover(data[pos:], mimeType, pictureType)
}

func makeID3Cover(data []byte, mimeType string, pictureType byte) (*ExtractedCover, int) {
	if len(data) == 0 || len(data) > maxEmbeddedCoverBytes {
		return nil, 0
	}
	if mimeType != "image/jpeg" && mimeType != "image/png" {
		if bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}) {
			mimeType = "image/jpeg"
		} else if bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
			mimeType = "image/png"
		} else {
			return nil, 0
		}
	}
	if mimeType == "image/jpeg" && !bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}) {
		return nil, 0
	}
	if mimeType == "image/png" && !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		return nil, 0
	}
	rank := 1
	if pictureType == 3 {
		rank = 2
	}
	return &ExtractedCover{Data: append([]byte(nil), data...), MimeType: mimeType, Ext: extensionForMime(mimeType), Source: "embedded_audiobook"}, rank
}

func coverMime(cover *ExtractedCover) string {
	if cover == nil {
		return ""
	}
	return cover.MimeType
}

func coverBytes(cover *ExtractedCover) int {
	if cover == nil {
		return 0
	}
	return len(cover.Data)
}

func validateAudioInput(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("audio path is required")
	}
	return safepath.ExistingUnderRoot(filepath.Dir(path), path)
}

func sanitizeAudioLogValue(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, value)
	value = strings.ReplaceAll(value, "\x1b", "")
	if len([]rune(value)) > 128 {
		value = string([]rune(value)[:128]) + "…"
	}
	return value
}

func sanitizeAudioLogValues(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = sanitizeAudioLogValue(value)
		value = strings.ReplaceAll(value, "\r", "")
		value = strings.ReplaceAll(value, "\n", "")
		cleaned = append(cleaned, value)
	}
	return cleaned
}

// readM4BMetadata reads common iTunes/MP4 metadata atoms without decoding
// audio. It is intentionally bounded; unsupported atoms are ignored and the
// filename/path fallback remains available.
func readM4BMetadata(path string) *AudioMeta {
	validatedPath, err := validateAudioInput(path)
	if err != nil {
		return nil
	}
	approvedRoot, rootErr := filepath.EvalSymlinks(filepath.Clean(filepath.Dir(path)))
	cleanCandidate := filepath.Clean(validatedPath)
	resolvedCandidate, candidateErr := filepath.EvalSymlinks(cleanCandidate)
	rel, relErr := filepath.Rel(approvedRoot, resolvedCandidate)
	if rootErr != nil || candidateErr != nil || relErr != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.ContainsAny(cleanCandidate, "\x00\r\n") {
		return nil
	}
	validatedPath = resolvedCandidate
	f, err := os.Open(validatedPath)
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
	name, abridged := cleanAudiobookFilenameStem(name)

	meta := &AudioMeta{Abridged: abridged}

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
	return normalized == "" || normalized == "incoming" || normalized == "downloads" || normalized == "unknown" || normalized == "unknown author" || normalized == "audiobook" || normalized == "audiobooks" || normalized == "audio" || regexp.MustCompile(`^\d+$`).MatchString(normalized)
}

func parseAudiobookFilename(path string) *AudioMeta {
	name := strings.TrimSpace(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	name, abridged := cleanAudiobookFilenameStem(name)
	meta := &AudioMeta{Abridged: abridged}
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

func cleanAudiobookFilenameStem(name string) (string, bool) {
	name = regexp.MustCompile(`(?i)\s*\[[^]]*(?:mp3|m4b|m4a|kbps|eng|english)[^]]*\]\s*$`).ReplaceAllString(name, "")
	abridged := false
	if regexp.MustCompile(`(?i)\(unabridged\)`).MatchString(name) {
		name = regexp.MustCompile(`(?i)\s*\(unabridged\)\s*[eE]?$`).ReplaceAllString(name, "")
	} else if regexp.MustCompile(`(?i)\(abridged\)`).MatchString(name) {
		abridged = true
		name = regexp.MustCompile(`(?i)\s*\(abridged\)\s*[eE]?$`).ReplaceAllString(name, "")
	}
	name = regexp.MustCompile(`(?i)\s*\((?:audiobook|retail)[^)]*\)\s*[eE]?$`).ReplaceAllString(name, "")
	name = strings.TrimSpace(strings.Trim(name, " ._-\t"))
	return name, abridged
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

	validatedDir, err := validateAudioInput(dirPath)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(validatedDir)
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
		entryPath, pathErr := validateAudioInput(filepath.Join(validatedDir, entry.Name()))
		if pathErr != nil {
			continue
		}
		meta := ExtractAudioMeta(entryPath)
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
	validatedPath, err := validateAudioInput(path)
	if err != nil {
		return nil
	}
	approvedRoot, rootErr := filepath.EvalSymlinks(filepath.Clean(filepath.Dir(path)))
	cleanCandidate := filepath.Clean(validatedPath)
	resolvedCandidate, candidateErr := filepath.EvalSymlinks(cleanCandidate)
	rel, relErr := filepath.Rel(approvedRoot, resolvedCandidate)
	if rootErr != nil || candidateErr != nil || relErr != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.ContainsAny(cleanCandidate, "\x00\r\n") {
		return nil
	}
	validatedPath = resolvedCandidate
	info, err := os.Stat(validatedPath)
	if err != nil {
		return nil
	}
	if info.IsDir() {
		_ = filepath.WalkDir(validatedPath, func(current string, entry os.DirEntry, walkErr error) error {
			if walkErr == nil && !entry.IsDir() && isAudiobookExtension(filepath.Ext(current)) {
				paths = append(paths, current)
			}
			return nil
		})
	} else if isAudiobookExtension(filepath.Ext(validatedPath)) {
		paths = append(paths, validatedPath)
	}
	sort.Strings(paths)
	return aggregateAudiobookMetadata(paths)
}

// ExtractAudiobookMetadataFromFiles aggregates known physical audiobook files
// without walking their parent directory. This is used when a legacy directory
// record has already been reconciled into individual normalized file rows.
func ExtractAudiobookMetadataFromFiles(paths []string) *AudiobookMeta {
	filtered := make([]string, 0, len(paths))
	for _, path := range paths {
		if isAudiobookExtension(filepath.Ext(path)) {
			filtered = append(filtered, path)
		}
	}
	sort.Strings(filtered)
	return aggregateAudiobookMetadata(filtered)
}

func aggregateAudiobookMetadata(paths []string) *AudiobookMeta {
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
