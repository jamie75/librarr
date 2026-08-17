package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jamie75/librarr/internal/download"
	"github.com/jamie75/librarr/internal/library"
	"github.com/jamie75/librarr/internal/models"
	"github.com/jamie75/librarr/internal/netutil"
	"github.com/jamie75/librarr/internal/search"
	wantedmeta "github.com/jamie75/librarr/internal/wanted"
)

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	var req models.DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   "Invalid request body",
		})
		return
	}

	if req.Title == "" {
		req.Title = "Unknown"
	}

	username, _ := r.Context().Value(ctxUsername).(string)
	s.db.LogActivity(username, "download_start", req.Title, fmt.Sprintf("Download started from %s", req.Source))

	source := s.searchMgr.GetSource(req.Source)
	if req.MediaType == "" {
		if source != nil {
			switch source.SearchTab() {
			case "audiobook", "manga":
				req.MediaType = source.SearchTab()
			}
		} else {
			switch req.Source {
			case "audiobook":
				req.MediaType = "audiobook"
			case "prowlarr_manga":
				req.MediaType = "manga"
			}
		}
	}

	// Determine download type.
	downloadType := "direct"
	if source != nil {
		downloadType = source.DownloadType()
	} else if req.Source == "torrent" || req.Source == "audiobook" || req.Source == "prowlarr_manga" {
		downloadType = "torrent"
	}

	// Check if this is an NZB result that should go to SABnzbd.
	if isNZBResult(req) && s.cfg.HasSABnzbd() {
		s.handleNZBDownload(w, req)
		return
	}

	switch downloadType {
	case "torrent":
		s.handleTorrentDownload(w, r, req)
	default:
		s.handleDirectDownloadReq(w, req)
	}
}

func (s *Server) handleTorrentDownload(w http.ResponseWriter, r *http.Request, req models.DownloadRequest) {
	if !s.cfg.HasTorrentClient() {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   "No torrent download client configured",
		})
		return
	}

	url, err := s.resolveTorrentURL(r.Context(), req, models.SearchResult{})
	if err != nil {
		safeTitle := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(req.Title, "\r", ""), "\n", ""), "\x00", "")
		safeError := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(netutil.SanitizeSensitiveText(err.Error()), "\r", ""), "\n", ""), "\x00", "")
		slog.Warn("torrent URL resolution failed", "title", netutil.SanitizeLogValue(safeTitle), "error", netutil.SanitizeLogValue(safeError))
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   "Failed to resolve download URL",
		})
		return
	}
	if url == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   "No download URL",
		})
		return
	}

	// Check for duplicates.
	sourceID := extractSourceID(req)
	if sourceID != "" && !req.Force && s.downloadMgr.HasSourceID(sourceID) {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"success": false,
			"error":   "Duplicate detected",
		})
		return
	}

	// Determine save path and category based on media type.
	savePath := s.cfg.QBSavePath
	category := s.cfg.QBCategory
	switch req.MediaType {
	case "audiobook":
		savePath = s.cfg.QBAudiobookSavePath
		category = s.cfg.QBAudiobookCategory
	case "manga":
		savePath = s.cfg.QBMangaSavePath
		category = s.cfg.QBMangaCategory
	}

	if wanted, identityErr := s.ensureDiscoverTorrentIdentity(&req); identityErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"success": false, "error": "Failed to preserve acquisition identity"})
		return
	} else if wanted != nil {
		req.Source = "wanted"
		req.SourceID = fmt.Sprintf("wanted:%d", wanted.ID)
		req.Title = wanted.Title
		req.Author = wanted.Author
	}

	tracked, err := s.downloadMgr.StartTorrentDownloadTracked(url, req.Title, savePath, category, req.InfoHash, req.MediaType, req.Source, extractSourceID(req))
	var verificationWarning *download.TorrentVerificationWarning
	if errors.As(err, &verificationWarning) {
		s.markWantedTrackedDownload(req, tracked)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": true,
			"title":   req.Title,
			"error":   "",
			"warning": errString(err),
		})
		return
	}
	status := http.StatusOK
	if err != nil {
		status = http.StatusBadGateway
		safeTitle := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(req.Title, "\r", ""), "\n", ""), "\x00", "")
		safeError := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(errString(err), "\r", ""), "\n", ""), "\x00", "")
		slog.Error("torrent submission failed", "title", netutil.SanitizeLogValue(safeTitle), "error", netutil.SanitizeLogValue(safeError))
	}
	if err == nil {
		s.markWantedTrackedDownload(req, tracked)
	}
	writeJSON(w, status, map[string]interface{}{
		"success": err == nil,
		"title":   req.Title,
		"error":   errString(err),
	})
}

// ensureDiscoverTorrentIdentity turns a direct Prowlarr acquisition into a
// durable Wanted identity before it reaches a torrent client. This avoids using
// volatile release names as canonical catalog metadata after completion.
func (s *Server) ensureDiscoverTorrentIdentity(req *models.DownloadRequest) (*models.WantedBook, error) {
	if req == nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.Source)), "prowlarr") {
		return nil, nil
	}
	candidate := wantedmeta.NormalizeBook(models.WantedBook{
		Title:              req.Title,
		Author:             req.Author,
		Source:             req.Source,
		OriginSource:       req.Source,
		OriginReleaseTitle: req.Title,
		SourceID:           extractSourceID(*req),
		MediaType:          req.MediaType,
		Monitored:          false,
		Status:             "wanted",
	}).Normalized
	// Prowlarr release tags are not contributor names. Normalization can retain
	// a trailing private-indexer tag after a multi-author string.
	if before, _, found := strings.Cut(candidate.Author, "["); found {
		candidate.Author = strings.TrimSpace(before)
	}
	if strings.TrimSpace(req.Author) == "" {
		if _, rawAuthor, found := strings.Cut(req.Title, " by "); found {
			if before, _, hasTag := strings.Cut(rawAuthor, "["); hasTag {
				rawAuthor = before
			}
			if rawAuthor = strings.TrimSpace(rawAuthor); rawAuthor != "" {
				candidate.Author = rawAuthor
			}
		}
	}
	if candidate.Title == "" || candidate.MediaType == "" {
		return nil, nil
	}
	items, err := s.db.ListWantedBooks()
	if err != nil {
		return nil, err
	}
	for i := range items {
		if strings.EqualFold(items[i].MediaType, candidate.MediaType) && library.TitleMatchKey(items[i].Title) == library.TitleMatchKey(candidate.Title) && library.ContributorMatchKey(items[i].Author) == library.ContributorMatchKey(candidate.Author) {
			return &items[i], nil
		}
	}
	return s.db.CreateWantedBook(candidate)
}

func (s *Server) markWantedTrackedDownload(req models.DownloadRequest, tracked models.TrackedDownload) {
	if !strings.HasPrefix(strings.ToLower(req.SourceID), "wanted:") {
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(strings.ToLower(req.SourceID), "wanted:"), 10, 64)
	if err != nil || id <= 0 {
		return
	}
	_, _ = s.db.MarkWantedDownloading(id, 0, req.Title, tracked.ClientID, tracked.InfoHash, time.Now().UTC())
}

func (s *Server) handleDirectDownloadReq(w http.ResponseWriter, req models.DownloadRequest) {
	// Anna's Archive download.
	if req.MD5 != "" {
		if !validateMD5(req.MD5) {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"success": false,
				"error":   "Invalid MD5 hash",
			})
			return
		}
		sourceID := req.MD5
		if !req.Force && s.downloadMgr.HasSourceID(sourceID) {
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"success": false,
				"error":   "Duplicate detected",
			})
			return
		}

		job, err := s.downloadMgr.StartAnnasDownload(req.MD5, req.Title)
		if err != nil {
			safeTitle := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(req.Title, "\r", ""), "\n", ""), "\x00", "")
			safeError := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(err.Error(), "\r", ""), "\n", ""), "\x00", "")
			slog.Error("anna's download start failed", "title", netutil.SanitizeLogValue(safeTitle), "error", netutil.SanitizeLogValue(safeError))
			writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
				"success": false,
				"error":   "Failed to start download",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": true,
			"job_id":  job.ID,
			"title":   req.Title,
		})
		return
	}

	// Generic URL download.
	if req.URL != "" || req.DownloadURL != "" {
		dlURL := req.URL
		if dlURL == "" {
			dlURL = req.DownloadURL
		}

		if err := netutil.ValidateOutboundURL(dlURL); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			})
			return
		}

		sourceID := extractSourceID(req)
		if sourceID != "" && !req.Force && s.downloadMgr.HasSourceID(sourceID) {
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"success": false,
				"error":   "Duplicate detected",
			})
			return
		}

		job, err := s.downloadMgr.StartDirectDownload(dlURL, req.Title, req.Source, sourceID, req.Author)
		if err != nil {
			safeTitle := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(req.Title, "\r", ""), "\n", ""), "\x00", "")
			safeError := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(err.Error(), "\r", ""), "\n", ""), "\x00", "")
			slog.Error("direct download start failed", "title", netutil.SanitizeLogValue(safeTitle), "error", netutil.SanitizeLogValue(safeError))
			writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
				"success": false,
				"error":   "Failed to start download",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": true,
			"job_id":  job.ID,
			"title":   req.Title,
		})
		return
	}

	writeJSON(w, http.StatusBadRequest, map[string]interface{}{
		"success": false,
		"error":   "No download source specified (need md5, url, or download_url)",
	})
}

func (s *Server) handleDownloadTorrent(w http.ResponseWriter, r *http.Request) {
	var req models.DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "Invalid request"})
		return
	}
	if req.Title == "" {
		req.Title = "Unknown"
	}
	req.MediaType = "ebook"
	s.handleTorrentDownload(w, r, req)
}

func (s *Server) handleDownloadAnnas(w http.ResponseWriter, r *http.Request) {
	var req models.DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "Invalid request"})
		return
	}
	if req.Title == "" {
		req.Title = "Unknown"
	}
	if req.MD5 == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "No MD5 hash"})
		return
	}
	if !validateMD5(req.MD5) {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "Invalid MD5 hash"})
		return
	}
	s.handleDirectDownloadReq(w, req)
}

func (s *Server) handleDownloadAudiobook(w http.ResponseWriter, r *http.Request) {
	var req models.DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "Invalid request"})
		return
	}
	if req.Title == "" {
		req.Title = "Unknown"
	}
	req.MediaType = "audiobook"
	s.handleTorrentDownload(w, r, req)
}

func (s *Server) handleGetDownloads(w http.ResponseWriter, _ *http.Request) {
	downloads := s.downloadMgr.GetDownloads()
	if downloads == nil {
		downloads = []models.DownloadStatus{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"downloads": downloads,
	})
}

func (s *Server) handleDeleteTorrent(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	err := s.downloadMgr.DeleteTorrent(hash)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": err == nil,
	})
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobID")
	err := s.downloadMgr.DeleteJob(jobID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{
			"success": false,
			"error":   "Job not found",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *Server) handleClearFinished(w http.ResponseWriter, _ *http.Request) {
	jobsCleared, torrentsCleared, err := s.downloadMgr.ClearFinished()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":          err == nil,
		"cleared_jobs":     jobsCleared,
		"cleared_torrents": torrentsCleared,
	})
}

func (s *Server) handleCheckDuplicate(w http.ResponseWriter, r *http.Request) {
	sourceID := r.URL.Query().Get("source_id")
	if sourceID == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"duplicate": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"duplicate": s.downloadMgr.HasSourceID(sourceID),
	})
}

func extractSourceID(req models.DownloadRequest) string {
	if req.SourceID != "" {
		return req.SourceID
	}
	if req.MD5 != "" {
		return req.MD5
	}
	if req.InfoHash != "" {
		return req.InfoHash
	}
	if req.GUID != "" {
		return req.GUID
	}
	if req.URL != "" {
		return req.URL
	}
	return ""
}

var resolveABBMagnetFn = search.ResolveABBMagnet

func (s *Server) resolveTorrentURL(ctx context.Context, req models.DownloadRequest, chosen models.SearchResult) (string, error) {
	if req.DownloadURL != "" {
		return req.DownloadURL, nil
	}
	if strings.HasPrefix(req.GUID, "magnet:") {
		return req.GUID, nil
	}
	if req.MagnetURL != "" {
		return req.MagnetURL, nil
	}
	if chosen.MagnetURL != "" {
		return chosen.MagnetURL, nil
	}
	if chosen.DownloadURL != "" {
		return chosen.DownloadURL, nil
	}
	if req.InfoHash != "" {
		return fmt.Sprintf("magnet:?xt=urn:btih:%s", req.InfoHash), nil
	}

	abbPath := req.AbbURL
	if abbPath == "" {
		abbPath = chosen.AbbURL
	}
	if abbPath == "" {
		return "", nil
	}

	resolved, err := s.resolveABBMagnet(ctx, abbPath)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func (s *Server) resolveABBMagnet(ctx context.Context, abbURL string) (string, error) {
	if s.cfg == nil || s.cfg.Sources == nil {
		return "", fmt.Errorf("AudioBookBay sources not configured")
	}

	abbPath := normalizeABBPath(abbURL)
	if abbPath == "" {
		return "", fmt.Errorf("invalid AudioBookBay URL")
	}

	client := &http.Client{Timeout: 15 * time.Second}
	return resolveABBMagnetFn(ctx, client, s.cfg.UserAgent, abbPath, s.cfg.Sources.AudioBookBay.Mirrors, s.cfg.Sources.AudioBookBay.Trackers)
}

func normalizeABBPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "/") {
		return raw
	}

	u, err := neturl.Parse(raw)
	if err != nil {
		if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
			return ""
		}
		return "/" + strings.TrimLeft(raw, "/")
	}
	if u.Scheme == "" && u.Host == "" {
		if strings.HasPrefix(u.Path, "/") {
			if u.RawQuery != "" {
				return u.Path + "?" + u.RawQuery
			}
			return u.Path
		}
		return "/" + strings.TrimLeft(u.Path, "/")
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return path
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return netutil.SanitizeSensitiveText(err.Error())
}

// isNZBResult checks if a download request should be routed to SABnzbd.
func isNZBResult(req models.DownloadRequest) bool {
	if req.DownloadProtocol == "nzb" {
		return true
	}
	dl := strings.ToLower(req.DownloadURL)
	return strings.HasSuffix(dl, ".nzb") ||
		strings.Contains(dl, "/nzb/") ||
		strings.Contains(dl, "nzb?") ||
		strings.Contains(dl, "&t=get&")
}

func (s *Server) handleNZBDownload(w http.ResponseWriter, req models.DownloadRequest) {
	if !s.cfg.HasSABnzbd() {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   "SABnzbd not configured",
		})
		return
	}

	nzbURL := req.DownloadURL
	if nzbURL == "" {
		nzbURL = req.URL
	}
	if nzbURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   "No NZB download URL",
		})
		return
	}

	nzoID, err := s.downloadMgr.StartNZBDownload(nzbURL, req.Title)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": err == nil,
		"title":   req.Title,
		"nzo_id":  nzoID,
		"error":   errString(err),
	})
}
