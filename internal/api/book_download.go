package api

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/jamie75/librarr/internal/applebooks"
	"github.com/jamie75/librarr/internal/library"
)

// handleV1BookDownload serves an authenticated, normalized-library source
// file. Selection and media-specific root validation live in the shared
// Apple Books delivery package so both delivery paths enforce the same policy.
func (s *Server) handleV1BookDownload(w http.ResponseWriter, r *http.Request) {
	if !s.ensureNormalizedReadAPI(w) {
		return
	}
	bookID, ok := parseIDPathValue(w, r, "id", "Invalid book ID")
	if !ok {
		return
	}
	if s.appleBooks == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "error": "Download delivery is unavailable"})
		return
	}
	fileID := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("file_id")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Invalid file ID"})
			return
		}
		fileID = parsed
	}
	delivery, err := s.appleBooks.PrepareDownload(r.Context(), bookID, fileID, r.PathValue("format"))
	if err != nil {
		status := http.StatusInternalServerError
		message := strings.TrimSpace(err.Error())
		if errors.Is(err, library.ErrNotFound) || strings.Contains(message, "not available") {
			status = http.StatusNotFound
		}
		if strings.Contains(message, "requested format") || strings.Contains(message, "not supported") || strings.Contains(message, "unsupported media type") || strings.Contains(message, "directory audiobook") || strings.Contains(message, "multi-track audiobooks") {
			status = http.StatusUnprocessableEntity
		}
		if errors.Is(err, library.ErrUnsafePath) || strings.Contains(message, "unsafe") {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{"success": false, "error": sanitizeAppleBooksErrorString(message)})
		return
	}
	// Recheck the returned path at the HTTP sink. This keeps path and symlink
	// validation visible at the operation that opens user/catalog-controlled data.
	realPath, safe := s.safeLibraryFilePath(delivery.Path)
	if !safe {
		writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": "Download source is outside the configured library roots"})
		return
	}
	file, err := os.Open(realPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": "Download source is unavailable"})
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": "Download source is unavailable"})
		return
	}
	filename := applebooks.SafeFilename(delivery.Filename)
	if filename == "" {
		filename = fmt.Sprintf("download.%s", delivery.Format)
	}
	w.Header().Set("Content-Type", delivery.ContentType)
	disposition, err := downloadContentDisposition(filename)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": "Download filename is invalid"})
		return
	}
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, filename, info.ModTime(), file)
}

func downloadContentDisposition(filename string) (string, error) {
	if filename == "" || strings.ContainsAny(filename, "\r\n\x00") {
		return "", errors.New("invalid download filename")
	}
	legacyFilename := strings.Map(func(char rune) rune {
		if char < 0x20 || char > 0x7e {
			return '_'
		}
		return char
	}, filename)
	legacy := mime.FormatMediaType("attachment", map[string]string{"filename": legacyFilename})
	if legacy == "" {
		return "", errors.New("invalid download filename")
	}
	return legacy + "; filename*=UTF-8''" + encodeRFC5987(filename), nil
}

func encodeRFC5987(value string) string {
	const attr = "!#$&+-.^_`|~"
	var encoded strings.Builder
	for _, b := range []byte(value) {
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune(attr, rune(b)) {
			encoded.WriteByte(b)
			continue
		}
		encoded.WriteByte('%')
		fmt.Fprintf(&encoded, "%02X", b)
	}
	return strings.ReplaceAll(encoded.String(), "+", "%20")
}
