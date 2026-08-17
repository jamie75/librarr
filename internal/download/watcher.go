package download

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jamie75/librarr/internal/config"
	"github.com/jamie75/librarr/internal/db"
	"github.com/jamie75/librarr/internal/library"
	libraryimport "github.com/jamie75/librarr/internal/library/import"
	"github.com/jamie75/librarr/internal/models"
	"github.com/jamie75/librarr/internal/organize"
	"github.com/jamie75/librarr/internal/search"
)

// Watcher monitors the active torrent client for completed torrents and runs
// the import pipeline.
type Watcher struct {
	cfg       *config.Config
	db        *db.DB
	torrent   TorrentClient
	organizer *organize.Organizer
	targets   *organize.LibraryTargets
	health    *search.HealthTracker
	importer  libraryimport.ImportEngine
	mappings  []RemotePathMapping
	registry  *ClientRegistry

	processing sync.Map // hash -> struct{}, tracks in-progress imports
	imported   sync.Map // hash -> struct{}, tracks already-imported hashes
	pending    sync.Map // hash/type -> reason signature, suppresses repeated pending INFO logs
	pollErrors sync.Map // client/category -> error signature, suppresses repeated listing failures
	stability  sync.Map // tracked download ID -> local content observation
}

var errTorrentContentPending = errors.New("torrent content pending synchronization")

const (
	trackedImportWaitingForSync = "waiting_for_sync"
	trackedImportNeedsReview    = "needs_review"
)

type trackedTorrentPoll struct {
	rowsReturned            int
	rowsAfterCategoryFilter int
	rows                    []TorrentInfo
	err                     error
	duration                time.Duration
}

// NewWatcher creates a new torrent completion watcher.
func NewWatcher(cfg *config.Config, database *db.DB, torrent TorrentClient, organizer *organize.Organizer, targets *organize.LibraryTargets, health *search.HealthTracker) *Watcher {
	return NewWatcherWithImportEngine(cfg, database, torrent, organizer, targets, health, nil)
}

func NewWatcherWithImportEngine(cfg *config.Config, database *db.DB, torrent TorrentClient, organizer *organize.Organizer, targets *organize.LibraryTargets, health *search.HealthTracker, importer libraryimport.ImportEngine) *Watcher {
	return &Watcher{
		cfg:       cfg,
		db:        database,
		torrent:   torrent,
		organizer: organizer,
		targets:   targets,
		health:    health,
		importer:  importer,
	}
}

func (w *Watcher) SetRemotePathMappings(mappings []RemotePathMapping) {
	w.mappings = append([]RemotePathMapping(nil), mappings...)
}

func (w *Watcher) SetClientRegistry(registry *ClientRegistry) {
	w.registry = registry
}

// Start begins the background watcher loop. It blocks until ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) {
	if w.torrent == nil {
		slog.Info("torrent watcher disabled (no torrent client configured)")
		return
	}

	slog.Info("torrent completion watcher started", "client", w.torrent.Name(), "interval", "30s")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Run once immediately.
	w.checkCompleted()

	for {
		select {
		case <-ctx.Done():
			slog.Info("torrent watcher stopping")
			return
		case <-ticker.C:
			w.checkCompleted()
		}
	}
}

func (w *Watcher) checkCompleted() {
	w.checkTracked()
	if clientType, ok := w.torrent.(interface{ Type() string }); ok && clientType.Type() == "rtorrent" {
		return
	}
	categories := []struct {
		name      string
		mediaType string
	}{
		{w.cfg.QBCategory, "ebook"},
		{w.cfg.QBAudiobookCategory, "audiobook"},
		{w.cfg.QBMangaCategory, "manga"},
	}

	for _, cat := range categories {
		torrents, err := w.torrent.GetTorrents(cat.name)
		if err != nil {
			continue
		}
		for _, t := range torrents {
			if t.Progress < 1.0 {
				continue
			}

			// Skip already imported.
			if _, ok := w.imported.Load(t.Hash); ok {
				continue
			}
			// Skip currently processing.
			if _, loaded := w.processing.LoadOrStore(t.Hash, struct{}{}); loaded {
				continue
			}

			go w.importTorrent(t, cat.mediaType)
		}
	}
}

func (w *Watcher) checkTracked() {
	pollStarted := time.Now()
	tracked, err := w.db.GetTrackedDownloads()
	if err != nil {
		slog.Warn("torrent watcher tracked-download query failed", "error", err)
		return
	}
	activeClient := w.torrent
	activeClientID := activeClient.Name()
	if identified, ok := activeClient.(interface{ ClientID() string }); ok {
		activeClientID = identified.ClientID()
	}
	pendingCount := 0
	for _, item := range tracked {
		if item.ImportStatus != "imported" {
			pendingCount++
		}
	}
	pollCache := make(map[string]trackedTorrentPoll)
	for _, item := range tracked {
		if item.ImportStatus == "imported" || item.ImportStatus == trackedImportNeedsReview {
			continue
		}
		pollClient := activeClient
		clientID := activeClientID
		if w.registry != nil {
			resolvedClient, resolveErr := w.registry.Resolve(item.ClientID)
			if resolveErr != nil {
				item.LastError = resolveErr.Error()
				_ = w.db.UpdateTrackedDownload(&item)
				continue
			}
			pollClient = resolvedClient
			clientID = item.ClientID
		} else if item.ClientID != clientID && item.ClientType != clientID {
			continue
		}
		pollKey := clientID + "/" + item.Category
		if typed, ok := pollClient.(interface{ Type() string }); ok && typed.Type() == "rtorrent" {
			// rTorrent is listed once from its complete main view; category is
			// only diagnostic context because labels are not a durable identity.
			pollKey = clientID + "/all"
		}
		result, ok := pollCache[pollKey]
		if !ok {
			result = w.pollTrackedTorrents(pollClient, item.Category)
			pollCache[pollKey] = result
			clientType := pollClient.Name()
			if typed, typedOK := pollClient.(interface{ Type() string }); typedOK {
				clientType = typed.Type()
			}
			fields := []any{
				"active_client_id", clientID,
				"active_client_type", clientType,
				"tracked_pending_downloads", pendingCount,
				"rows_returned", result.rowsReturned,
				"rows_after_category_filter", result.rowsAfterCategoryFilter,
				"requested_category", item.Category,
				"poll_duration_ms", result.duration.Milliseconds(),
			}
			if result.err != nil {
				fields = append(fields, "list_error", result.err.Error())
				w.logTorrentPollFailure(pollKey, result.err)
			} else {
				fields = append(fields, "list_error", "")
				w.logTorrentPollRecovery(pollKey)
			}
			slog.Debug("torrent watcher poll", fields...)
		}
		if result.err != nil {
			// Listing failures are transport failures, not evidence that this
			// torrent or its import failed. Keep its last observed status and let
			// the bounded watcher cadence retry the shared client poll.
			item.LastError = retryableTorrentPollError(result.err)
			_ = w.db.UpdateTrackedDownload(&item)
			continue
		}
		var match *TorrentInfo
		storedHashes := []string{normalizeTorrentHash(item.InfoHash), normalizeTorrentHash(item.DownloadID)}
		for i := range result.rows {
			returnedHash := normalizeTorrentHash(result.rows[i].Hash)
			matched := returnedHash != "" && (returnedHash == storedHashes[0] || returnedHash == storedHashes[1])
			slog.Debug("torrent watcher hash matching evidence",
				"stored_hash", item.InfoHash,
				"returned_hash", result.rows[i].Hash,
				"normalized_stored_hash", storedHashes[0],
				"normalized_returned_hash", returnedHash,
				"client_id", clientID,
				"category", item.Category,
				"matched", matched,
				"skip_reason", map[bool]string{true: "", false: "hash mismatch"}[matched],
			)
			if matched {
				match = &result.rows[i]
				break
			}
		}
		if match == nil {
			slog.Debug("tracked torrent hash not found in client listing",
				"stored_hash", item.InfoHash,
				"normalized_stored_hash", storedHashes[0],
				"client_id", clientID,
				"category", item.Category,
				"rows_returned", len(result.rows),
				"skip_reason", "no matching hash",
			)
			item.LastError = "tracked download is not currently visible in the configured client"
			_ = w.db.UpdateTrackedDownload(&item)
			continue
		}
		previousStatus := item.Status
		previousObserved := item.LastObservedStatus
		item.LastObservedStatus = match.State
		item.Progress = match.Progress
		item.Status = MapTorrentStatus(match.State)
		if previousObserved == "" {
			slog.Info("tracked torrent first observed", "torrent_hash", match.Hash, "name", match.Name, "state", match.State, "category", item.Category)
		} else if previousObserved != match.State {
			slog.Info("tracked torrent state transition", "torrent_hash", match.Hash, "from", previousObserved, "to", match.State, "progress", match.Progress)
		}
		complete := match.State == "completed" || match.Progress >= 1
		if complete {
			item.Progress = 1
			item.Status = "completed"
			if previousStatus != "completed" {
				slog.Info("tracked torrent completion detected", "torrent_hash", match.Hash, "name", match.Name, "category", item.Category)
			}
		}
		if !complete {
			_ = w.db.UpdateTrackedDownload(&item)
			continue
		}
		if item.CompletedAt == nil {
			now := time.Now().UTC()
			item.CompletedAt = &now
		}
		resolved := ResolveRemotePath(item.ClientID, firstNonEmpty(match.ContentPath, match.SavePath), w.mappings)
		if resolvedPath := firstNonEmpty(match.ContentPath, match.SavePath); filepath.IsAbs(resolvedPath) {
			clientType, _ := pollClient.(interface{ Type() string })
			safeExact := clientType == nil || clientType.Type() != "rtorrent" || localPathMatchesClientMapping(item.ClientID, resolvedPath, w.mappings)
			if safeExact {
				if _, statErr := os.Stat(resolvedPath); statErr == nil {
					resolved = PathResolution{Strategy: "reported_path_exists", ClientID: item.ClientID, ReportedPath: resolvedPath, ResolvedPath: resolvedPath, Exists: true}
				}
			}
		}
		if resolved.ResolvedPath != "" && !resolved.Exists && (resolved.Strategy == "reported_path_exists" || localPathMatchesClientMapping(item.ClientID, resolved.ResolvedPath, w.mappings)) {
			if _, statErr := os.Stat(resolved.ResolvedPath); statErr == nil {
				resolved.Exists = true
			} else {
				resolved.FailureReason = localContentAvailabilityReason(statErr)
			}
		}
		// Preserve the established qBittorrent/Transmission fallback while
		// requiring explicit client-scoped mappings for rTorrent.
		if !resolved.Exists {
			if clientType, ok := pollClient.(interface{ Type() string }); !ok || clientType.Type() != "rtorrent" {
				legacy := (&Watcher{cfg: w.cfg, torrent: pollClient}).resolveLocalPathResult(*match, item.MediaType)
				if legacy.Failure == "" {
					resolved = PathResolution{Strategy: legacy.Strategy, ClientID: item.ClientID, ReportedPath: legacy.ReportedPath, ResolvedPath: legacy.Path, Exists: pathExists(legacy.Path), FailureReason: legacy.Failure}
				}
			}
		}
		item.RemotePath = resolved.ReportedPath
		item.LocalPath = resolved.ResolvedPath
		slog.Debug("tracked download path mapping",
			"client_id", item.ClientID, "client_type", item.ClientType,
			"torrent_hash", item.InfoHash, "torrent_name", item.Title,
			"reported_path", resolved.ReportedPath, "strategy", resolved.Strategy,
			"matched_remote_prefix", resolved.MatchedRemote, "local_prefix", resolved.LocalPrefix,
			"resolved_path", resolved.ResolvedPath, "exists", resolved.Exists,
			"failure_reason", resolved.FailureReason)
		if !resolved.Exists {
			item.ImportStatus = trackedImportWaitingForSync
			item.LastError = firstNonEmpty(resolved.FailureReason, "waiting for local content")
			w.logTrackedSyncPending(item, resolved)
			_ = w.db.UpdateTrackedDownload(&item)
			continue
		}
		stable, reason := w.localContentIsStable(item.ID, resolved.ResolvedPath, item.MediaType)
		if !stable {
			if strings.Contains(reason, "unsupported") || strings.Contains(reason, "no supported") {
				item.ImportStatus = trackedImportNeedsReview
				item.LastError = reason
				slog.Warn("tracked torrent content requires manual review", "torrent_hash", item.InfoHash, "local_path", item.LocalPath, "reason", reason)
				_ = w.db.UpdateTrackedDownload(&item)
				continue
			}
			item.ImportStatus = trackedImportWaitingForSync
			item.LastError = reason
			w.logTrackedSyncPending(item, PathResolution{
				ClientID: item.ClientID, ReportedPath: resolved.ReportedPath,
				MatchedRemote: resolved.MatchedRemote, ResolvedPath: resolved.ResolvedPath,
				Exists: true, FailureReason: reason,
			})
			_ = w.db.UpdateTrackedDownload(&item)
			continue
		}
		if _, wasPending := w.pending.LoadAndDelete("tracked-sync:" + item.InfoHash + ":" + item.MediaType); wasPending {
			slog.Info("tracked torrent mapped local file found", "torrent_hash", item.InfoHash, "remote_path", item.RemotePath, "local_path", item.LocalPath)
		}
		if _, loaded := w.processing.LoadOrStore(item.InfoHash, struct{}{}); loaded {
			continue
		}
		item.ImportStatus = "importing"
		item.LastError = ""
		if err := w.db.UpdateTrackedDownload(&item); err != nil {
			w.processing.Delete(item.InfoHash)
			slog.Error("tracked torrent import state update failed", "torrent_hash", item.InfoHash, "error", err)
			continue
		}
		slog.Info("tracked torrent import started", "torrent_hash", item.InfoHash, "name", item.Title, "local_path", item.LocalPath)
		go w.importTracked(item, *match)
	}
	slog.Debug("torrent watcher poll completed", "active_client_id", activeClientID, "tracked_pending_downloads", pendingCount, "poll_duration_ms", time.Since(pollStarted).Milliseconds())
}

func (w *Watcher) pollTrackedTorrents(client TorrentClient, category string) trackedTorrentPoll {
	started := time.Now()
	result := trackedTorrentPoll{}
	if readonly, ok := client.(ReadOnlyDownloadClient); ok && readonly.Type() == "rtorrent" {
		downloads, err := readonly.ListDownloads(context.Background())
		result.duration = time.Since(started)
		if err != nil {
			result.err = err
			return result
		}
		result.rows = make([]TorrentInfo, 0, len(downloads))
		for _, download := range downloads {
			result.rows = append(result.rows, TorrentInfo{
				Name: download.Name, ContentPath: download.ContentPath, SavePath: download.SavePath,
				Hash: download.InfoHash, State: download.Status, Progress: download.Progress,
				TotalSize: download.Size, Category: download.Label,
			})
		}
		result.rowsReturned = len(result.rows)
		// A durable tracked hash must remain visible even if custom1 is empty
		// or has changed, so rTorrent rows intentionally bypass label filtering.
		result.rowsAfterCategoryFilter = len(result.rows)
		return result
	}
	rows, err := client.GetTorrents(category)
	result.duration = time.Since(started)
	result.err = err
	result.rows = rows
	result.rowsReturned = len(rows)
	result.rowsAfterCategoryFilter = len(rows)
	return result
}

func normalizeTorrentHash(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func (w *Watcher) logTrackedSyncPending(item models.TrackedDownload, resolved PathResolution) {
	key := "tracked-sync:" + item.InfoHash + ":" + item.MediaType
	signature := resolved.ResolvedPath + ":" + resolved.FailureReason
	fields := []any{"torrent_hash", item.InfoHash, "remote_path", resolved.ReportedPath, "selected_mapping", resolved.MatchedRemote, "suffix", strings.TrimPrefix(resolved.ReportedPath, resolved.MatchedRemote), "local_path", resolved.ResolvedPath, "local_exists", resolved.Exists}
	if previous, ok := w.pending.Load(key); ok && previous == signature {
		slog.Debug("waiting for synchronized torrent file", fields...)
		return
	}
	w.pending.Store(key, signature)
	slog.Info("waiting for synchronized torrent file", fields...)
}

func (w *Watcher) logTorrentPollFailure(key string, err error) {
	signature := err.Error()
	if previous, loaded := w.pollErrors.Load(key); loaded && previous == signature {
		slog.Debug("torrent watcher listing still failing", "scope", key, "error", err)
		return
	}
	w.pollErrors.Store(key, signature)
	slog.Warn("torrent watcher listing failed", "scope", key, "error", err)
}

func (w *Watcher) logTorrentPollRecovery(key string) {
	if _, loaded := w.pollErrors.LoadAndDelete(key); loaded {
		slog.Info("torrent watcher listing recovered", "scope", key)
	}
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func localPathMatchesClientMapping(clientID, candidate string, mappings []RemotePathMapping) bool {
	candidate = cleanPath(candidate)
	for _, mapping := range mappings {
		if !mapping.Enabled || !strings.EqualFold(mapping.ClientID, clientID) {
			continue
		}
		localRoot := cleanPath(mapping.LocalPath)
		if !pathWithin(candidate, localRoot) {
			continue
		}
		resolvedCandidate, candidateErr := filepath.EvalSymlinks(candidate)
		resolvedRoot, rootErr := filepath.EvalSymlinks(localRoot)
		if candidateErr == nil && rootErr == nil {
			return pathWithin(resolvedCandidate, resolvedRoot)
		}
		return true
	}
	return false
}

func (w *Watcher) importTracked(item models.TrackedDownload, torrent TorrentInfo) {
	defer w.processing.Delete(item.InfoHash)
	localPath := item.LocalPath
	torrent.ContentPath = localPath
	mediaType := item.MediaType
	canonical := w.canonicalTrackedImportMetadata(item)
	if err := func() error {
		switch mediaType {
		case "audiobook":
			return w.importAudiobookWithMetadata(torrent, localPath, canonical)
		case "manga":
			return w.importMangaWithMetadata(torrent, localPath, canonical)
		default:
			return w.importEbookWithMetadata(torrent, localPath, canonical)
		}
	}(); err != nil {
		if errors.Is(err, errTorrentContentPending) {
			item.Status = "completed"
			item.ImportStatus = trackedImportWaitingForSync
			item.LastError = "waiting for local content"
			slog.Info("tracked torrent import deferred", "torrent_hash", item.InfoHash, "local_path", item.LocalPath)
			if updateErr := w.db.UpdateTrackedDownload(&item); updateErr != nil {
				slog.Error("tracked torrent deferred state update failed", "torrent_hash", item.InfoHash, "error", updateErr)
			}
			return
		}
		if strings.Contains(strings.ToLower(err.Error()), "manual review") {
			item.ImportStatus = trackedImportNeedsReview
			item.LastError = "import requires manual review"
			slog.Warn("tracked torrent import requires manual review", "torrent_hash", item.InfoHash, "local_path", item.LocalPath)
			if updateErr := w.db.UpdateTrackedDownload(&item); updateErr != nil {
				slog.Error("tracked torrent review state update failed", "torrent_hash", item.InfoHash, "error", updateErr)
			}
			return
		}
		item.ImportStatus = "failed"
		item.LastError = err.Error()
		slog.Error("tracked torrent import failed", "torrent_hash", item.InfoHash, "local_path", item.LocalPath, "error", err)
		if updateErr := w.db.UpdateTrackedDownload(&item); updateErr != nil {
			slog.Error("tracked torrent failure state update failed", "torrent_hash", item.InfoHash, "error", updateErr)
		}
		return
	}
	reconcileWantedImport(w.db, item.SourceID, item.Title, "", library.MediaType(mediaType), nil)
	now := time.Now().UTC()
	item.ImportStatus = "imported"
	item.Status = "imported"
	item.ImportedAt = &now
	item.LastError = ""
	if err := w.db.UpdateTrackedDownload(&item); err != nil {
		slog.Error("tracked torrent imported state update failed", "torrent_hash", item.InfoHash, "error", err)
		return
	}
	slog.Info("tracked torrent import completed", "torrent_hash", item.InfoHash, "name", item.Title, "local_path", item.LocalPath)
	w.imported.Store(item.InfoHash, struct{}{})
}

func (w *Watcher) importTorrent(t TorrentInfo, mediaType string) {
	defer w.processing.Delete(t.Hash)

	slog.Debug("processing completed torrent", "name", t.Name, "hash", t.Hash, "type", mediaType)

	resolved := w.resolveLocalPathResult(t, mediaType)
	savePath := resolved.Path
	w.logPathResolution(t, mediaType, resolved)

	var importErr error
	if _, err := os.Stat(savePath); err != nil {
		if os.IsNotExist(err) {
			w.logImportPending(t, mediaType, savePath, "local synchronized path not available", nil)
		} else {
			w.logImportPending(t, mediaType, savePath, "local path stat failed", err)
		}
		return
	}
	switch mediaType {
	case "ebook":
		importErr = w.importEbook(t, savePath)
	case "audiobook":
		importErr = w.importAudiobook(t, savePath)
	case "manga":
		importErr = w.importManga(t, savePath)
	}

	if importErr != nil {
		if errors.Is(importErr, errTorrentContentPending) {
			w.logImportPending(t, mediaType, savePath, "content not ready for import", importErr)
		} else {
			w.clearImportPending(t.Hash, mediaType)
			slog.Error("torrent import failed", "name", t.Name, "type", mediaType, "error", importErr)
		}
		return
	}

	// Optionally remove torrent from qBit after import. Default is to remove;
	// set REMOVE_TORRENT_AFTER_IMPORT=false to keep seeding (e.g. private trackers).
	if w.cfg.RemoveTorrentAfterImport {
		if err := w.torrent.DeleteTorrent(t.Hash, false); err != nil {
			slog.Warn("failed to remove torrent after import", "hash", t.Hash, "error", err)
		} else {
			slog.Info("removed completed torrent", "name", t.Name)
		}
	} else {
		slog.Info("torrent left seeding after import", "name", t.Name, "hash", t.Hash)
	}

	// Mark as imported.
	w.imported.Store(t.Hash, struct{}{})
	w.clearImportPending(t.Hash, mediaType)

	// Log the import.
	if err := w.db.LogEvent("torrent_import", t.Name, fmt.Sprintf("Imported %s from torrent", mediaType), nil, t.Hash); err != nil {
		slog.Warn("failed to log torrent import", "name", t.Name, "hash", t.Hash, "error", err)
	}
}

// resolveLocalPath maps qBittorrent container paths to local paths.
// Each media type resolves to Librarr's local synchronized incoming mount,
// not the remote qBittorrent save path or the final organized library
// directory. qBittorrent may report paths from a seedbox filesystem that
// Librarr can only see by basename under INCOMING_DIR after rclone sync.
func (w *Watcher) resolveLocalPath(t TorrentInfo, mediaType string) string {
	return w.resolveLocalPathResult(t, mediaType).Path
}

type localPathResolution struct {
	Path         string
	Strategy     string
	ReportedPath string
	IncomingDir  string
	Failure      string
}

func (w *Watcher) resolveLocalPathResult(t TorrentInfo, mediaType string) localPathResolution {
	var rootName string
	localIncoming := w.incomingDirForMedia(mediaType)

	if t.ContentPath != "" {
		return w.resolveContentPath(t, mediaType)
	}

	// Fetch files from qBittorrent to find the actual root folder/file.
	var files []TorrentFile
	var err error
	if w.torrent != nil {
		files, err = w.torrent.GetTorrentFiles(t.Hash)
	}
	if err == nil && len(files) > 0 {
		var firstPart string
		allSameRoot := true
		for i, f := range files {
			parts := strings.Split(f.Name, "/")
			if len(parts) > 0 {
				if i == 0 {
					firstPart = parts[0]
				} else if firstPart != parts[0] {
					allSameRoot = false
					break
				}
			}
		}
		if allSameRoot && firstPart != "" {
			rootName = normalizeTorrentPath(firstPart)
		}
	}

	if rootName == "" {
		rootName = normalizeTorrentPath(t.Name)
	}

	path, ok := joinWithinRoot(localIncoming, rootName)
	if !ok {
		return localPathResolution{
			Path:         localIncoming,
			Strategy:     "rejected_torrent_name_or_files",
			ReportedPath: t.Name,
			IncomingDir:  localIncoming,
			Failure:      "torrent name or file root could not be resolved safely",
		}
	}
	return localPathResolution{
		Path:         path,
		Strategy:     "torrent_name_or_files",
		ReportedPath: t.Name,
		IncomingDir:  localIncoming,
	}
}

func (w *Watcher) incomingDirForMedia(mediaType string) string {
	switch mediaType {
	case "audiobook":
		return w.cfg.IncomingDir
	case "manga":
		if w.cfg.MangaIncomingDir != "" {
			return w.cfg.MangaIncomingDir
		}
		return w.cfg.IncomingDir
	default:
		return w.cfg.IncomingDir
	}
}

func (w *Watcher) remoteSavePathForMedia(mediaType string) string {
	switch mediaType {
	case "audiobook":
		return w.cfg.QBAudiobookSavePath
	case "manga":
		return w.cfg.QBMangaSavePath
	default:
		return w.cfg.QBSavePath
	}
}

func (w *Watcher) resolveContentPath(t TorrentInfo, mediaType string) localPathResolution {
	contentPath := normalizeTorrentPath(t.ContentPath)
	contentPath = filepath.Clean(contentPath)
	savePath := filepath.Clean(normalizeTorrentPath(t.SavePath))
	localIncoming := w.incomingDirForMedia(mediaType)

	if contentPath == "" {
		return localPathResolution{Path: localIncoming, Strategy: "empty_content_path", ReportedPath: t.ContentPath, IncomingDir: localIncoming, Failure: "empty content path"}
	}

	if filepath.IsAbs(contentPath) {
		if _, err := os.Stat(contentPath); err == nil {
			return localPathResolution{Path: contentPath, Strategy: "reported_path_exists", ReportedPath: t.ContentPath, IncomingDir: localIncoming}
		}
	}

	remoteRoots := []string{savePath, filepath.Clean(normalizeTorrentPath(w.remoteSavePathForMedia(mediaType)))}
	for _, remoteRoot := range remoteRoots {
		if mapped, ok := mapTorrentPath(contentPath, remoteRoot, localIncoming); ok {
			return localPathResolution{Path: mapped, Strategy: "mapped_remote_root", ReportedPath: t.ContentPath, IncomingDir: localIncoming}
		}
	}

	if mapped, ok := mapTorrentPath(contentPath, localIncoming, localIncoming); ok {
		return localPathResolution{Path: mapped, Strategy: "already_under_incoming", ReportedPath: t.ContentPath, IncomingDir: localIncoming}
	}

	if !filepath.IsAbs(contentPath) && (contentPath == ".." || strings.HasPrefix(contentPath, ".."+string(os.PathSeparator))) {
		return localPathResolution{Path: localIncoming, Strategy: "rejected_relative_content_path", ReportedPath: t.ContentPath, IncomingDir: localIncoming, Failure: "relative content path escaped incoming root"}
	}

	if !filepath.IsAbs(contentPath) {
		if joined, ok := joinWithinRoot(localIncoming, contentPath); ok {
			return localPathResolution{Path: joined, Strategy: "relative_content_path", ReportedPath: t.ContentPath, IncomingDir: localIncoming}
		}
		return localPathResolution{Path: localIncoming, Strategy: "rejected_relative_content_path", ReportedPath: t.ContentPath, IncomingDir: localIncoming, Failure: "relative content path escaped incoming root"}
	}

	base := filepath.Base(contentPath)
	if joined, ok := joinWithinRoot(localIncoming, base); ok {
		return localPathResolution{Path: joined, Strategy: "incoming_basename_fallback", ReportedPath: t.ContentPath, IncomingDir: localIncoming}
	}
	return localPathResolution{Path: localIncoming, Strategy: "rejected_content_path", ReportedPath: t.ContentPath, IncomingDir: localIncoming, Failure: "content path could not be resolved safely"}
}

// mapTorrentPath translates a path reported by the remote torrent client into
// the corresponding locally mounted path. It only succeeds for paths at or
// below remoteRoot, preventing traversal and unrelated absolute paths from
// escaping localRoot.
func mapTorrentPath(reportedPath, remoteRoot, localRoot string) (string, bool) {
	reportedPath = filepath.Clean(normalizeTorrentPath(reportedPath))
	remoteRoot = filepath.Clean(normalizeTorrentPath(remoteRoot))
	localRoot = filepath.Clean(normalizeTorrentPath(localRoot))
	if reportedPath == "." || remoteRoot == "." || localRoot == "." ||
		!filepath.IsAbs(reportedPath) || !filepath.IsAbs(remoteRoot) || !filepath.IsAbs(localRoot) {
		return "", false
	}

	rel, err := filepath.Rel(remoteRoot, reportedPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return filepath.Join(localRoot, rel), true
}

func joinWithinRoot(root, relPath string) (string, bool) {
	root = filepath.Clean(normalizeTorrentPath(root))
	relPath = filepath.Clean(normalizeTorrentPath(relPath))
	if root == "." || relPath == "." || relPath == "" || filepath.IsAbs(relPath) || !filepath.IsAbs(root) {
		return "", false
	}
	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(os.PathSeparator)) {
		return "", false
	}
	candidate := filepath.Join(root, relPath)
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return candidate, true
}

func (w *Watcher) logPathResolution(t TorrentInfo, mediaType string, resolved localPathResolution) {
	fields := []any{
		"name", t.Name,
		"hash", t.Hash,
		"type", mediaType,
		"reported_path", resolved.ReportedPath,
		"incoming_dir", resolved.IncomingDir,
		"strategy", resolved.Strategy,
		"resolved_path", resolved.Path,
	}
	if resolved.Failure != "" {
		slog.Warn("torrent local path resolution warning", append(fields, "failure", resolved.Failure)...)
		return
	}
	slog.Debug("torrent local path resolved", fields...)
}

func (w *Watcher) logImportPending(t TorrentInfo, mediaType, path, reason string, err error) {
	fields := []any{
		"name", t.Name,
		"hash", t.Hash,
		"type", mediaType,
		"path", path,
		"reason", reason,
	}
	if err != nil {
		fields = append(fields, "error", err)
	}

	key := t.Hash + ":" + mediaType
	signature := path + ":" + reason
	if err != nil {
		signature += ":" + err.Error()
	}
	if previous, ok := w.pending.Load(key); ok && previous == signature {
		slog.Debug("torrent import still pending", fields...)
		return
	}
	w.pending.Store(key, signature)
	if err != nil && !errors.Is(err, errTorrentContentPending) {
		slog.Warn("torrent import pending", fields...)
		return
	}
	slog.Info("torrent import pending", fields...)
}

func (w *Watcher) clearImportPending(hash, mediaType string) {
	w.pending.Delete(hash + ":" + mediaType)
}

func (w *Watcher) importEbook(t TorrentInfo, savePath string) error {
	return w.importEbookWithMetadata(t, savePath, trackedImportMetadata{})
}

func (w *Watcher) importEbookWithMetadata(t TorrentInfo, savePath string, canonical trackedImportMetadata) error {
	bookFiles := findFilesByExt(savePath, []string{".epub", ".mobi", ".pdf", ".azw3"})
	if len(bookFiles) == 0 {
		return fmt.Errorf("%w: no ebook files found at %s", errTorrentContentPending, savePath)
	}

	for _, bf := range bookFiles {
		metadata := organize.ExtractEbookMetadata(bf)
		embeddedMetadata := organize.ExtractEmbeddedEbookMetadata(bf)
		title := firstNonEmpty(canonical.override.SelectedTitle, metadata.Title, t.Name)
		author := firstNonEmpty(canonical.override.SelectedAuthor, metadata.Author)
		metadataSource := firstNonEmpty(canonical.source, ebookMetadataSource(embeddedMetadata))
		destPath, err := w.organizer.OrganizeEbook(bf, title, author)
		if err != nil {
			return fmt.Errorf("%w: organize ebook %q: %v", errTorrentContentPending, bf, err)
		}

		inserted, err := w.importTorrentItemWithMetadata(context.Background(), t, library.MediaTypeEbook, bf, destPath, title, author, metadata.Title, metadata.Author, fileFormat(destPath), t.TotalSize, canonical.override, metadataSource)
		if err != nil {
			return err
		}

		// Import to external libraries.
		if inserted && w.targets != nil {
			w.targets.ImportEbook(destPath, t.Name, author)
		}
	}

	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (w *Watcher) importAudiobook(t TorrentInfo, savePath string) error {
	return w.importAudiobookWithMetadata(t, savePath, trackedImportMetadata{})
}

func (w *Watcher) importAudiobookWithMetadata(t TorrentInfo, savePath string, canonical trackedImportMetadata) error {
	// If the source path doesn't even exist, fail the import.
	if _, statErr := os.Stat(savePath); os.IsNotExist(statErr) {
		return fmt.Errorf("%w: source path does not exist: %s", errTorrentContentPending, savePath)
	}

	aggregate := organize.ExtractAudiobookMetadata(savePath)
	embedded := libraryimport.CandidateMetadata{}
	if aggregate != nil && aggregate.Embedded {
		embedded.SelectedTitle = strings.TrimSpace(aggregate.Title)
		embedded.SelectedAuthor = strings.TrimSpace(aggregate.Author)
		embedded.Narrator = strings.TrimSpace(aggregate.Narrator)
		embedded.DurationSeconds = aggregate.DurationSeconds
		embedded.TrackCount = aggregate.TrackCount
		embedded.ChapterCount = aggregate.ChapterCount
		embedded.Abridged = aggregate.Abridged
	}
	title, author, metadataSource := chooseTrackedAudiobookMetadata(t.Name, &embedded, canonical)
	override := canonical.override
	if override.SelectedTitle == "" {
		override.SelectedTitle = embedded.SelectedTitle
	}
	if override.SelectedAuthor == "" {
		override.SelectedAuthor = embedded.SelectedAuthor
	}
	if override.Narrator == "" {
		override.Narrator = embedded.Narrator
	}
	if override.DurationSeconds == 0 {
		override.DurationSeconds = embedded.DurationSeconds
	}
	if override.TrackCount == 0 {
		override.TrackCount = embedded.TrackCount
	}
	if override.ChapterCount == 0 {
		override.ChapterCount = embedded.ChapterCount
	}
	override.Abridged = override.Abridged || embedded.Abridged
	slog.Info("tracked audiobook metadata selected", append([]any{"torrent_hash", t.Hash}, trackedMetadataLogFields(canonical, title, author)...)...)

	destPath, err := w.organizer.OrganizeAudiobook(savePath, title, author)
	if err != nil {
		return fmt.Errorf("organize audiobook %q: %w", savePath, err)
	}

	inserted, err := w.importTorrentItemWithMetadata(context.Background(), t, library.MediaTypeAudiobook, savePath, destPath, title, author, embedded.SelectedTitle, embedded.SelectedAuthor, fileFormat(destPath), t.TotalSize, override, metadataSource)
	if err != nil {
		return err
	}

	if inserted && w.targets != nil {
		w.targets.ImportAudiobook()
	}

	return nil
}

func (w *Watcher) importManga(t TorrentInfo, savePath string) error {
	return w.importMangaWithMetadata(t, savePath, trackedImportMetadata{})
}

func (w *Watcher) importMangaWithMetadata(t TorrentInfo, savePath string, canonical trackedImportMetadata) error {
	mangaFiles := findFilesByExt(savePath, []string{".cbz", ".cbr", ".zip", ".pdf", ".epub"})
	if len(mangaFiles) == 0 {
		return fmt.Errorf("%w: no manga files found at %s", errTorrentContentPending, savePath)
	}

	for _, mf := range mangaFiles {
		title := firstNonEmpty(canonical.override.SelectedTitle, t.Name)
		destPath, err := w.organizer.OrganizeManga(mf, title)
		if err != nil {
			return fmt.Errorf("%w: organize manga %q: %v", errTorrentContentPending, mf, err)
		}

		inserted, err := w.importTorrentItemWithMetadata(context.Background(), t, library.MediaTypeManga, mf, destPath, title, "", title, "", fileFormat(destPath), t.TotalSize, canonical.override, firstNonEmpty(canonical.source, "torrent_name"))
		if err != nil {
			return err
		}

		if inserted && w.targets != nil {
			w.targets.ImportManga(destPath, t.Name)
		}
	}

	return nil
}

func (w *Watcher) recordTorrentItem(t TorrentInfo, mediaType, sourcePath, destinationPath, title, author, metadataTitle, metadataAuthor, format string, fileSize int64) (bool, error) {
	return w.recordTorrentItemWithMetadata(t, mediaType, sourcePath, destinationPath, title, author, metadataTitle, metadataAuthor, format, fileSize, "torrent_name")
}

func (w *Watcher) recordTorrentItemWithMetadata(t TorrentInfo, mediaType, sourcePath, destinationPath, title, author, metadataTitle, metadataAuthor, format string, fileSize int64, metadataSource string) (bool, error) {
	if info, err := os.Stat(destinationPath); err == nil && info.Mode().IsRegular() {
		fileSize = info.Size()
	}
	item := &models.LibraryItem{
		Title:        title,
		Author:       author,
		FilePath:     destinationPath,
		OriginalPath: sourcePath,
		FileSize:     fileSize,
		FileFormat:   format,
		MediaType:    mediaType,
		Source:       "torrent",
		SourceID:     t.Hash,
	}
	outcome, err := w.db.AddItemWithOutcome(item)
	fields := []any{
		"torrent_hash", t.Hash,
		"torrent_name", t.Name,
		"source_path", sourcePath,
		"normalized_source_path", db.NormalizeLibraryPath(sourcePath),
		"destination_path", destinationPath,
		"normalized_destination_path", outcome.NormalizedPath,
		"file_size", fileSize,
		"detected_format", format,
		"metadata_title", metadataTitle,
		"metadata_author", metadataAuthor,
		"metadata_source", metadataSource,
		"content_hash", outcome.ContentHash,
		"existing_record_id", outcome.ExistingID,
		"duplicate_decision", outcome.Reason,
		"database_decision", map[bool]string{true: "insert", false: "reuse_existing"}[outcome.Inserted],
	}
	if err != nil {
		slog.Error("torrent library import database failure", append(fields, "error", err)...)
		return false, err
	}
	slog.Info("torrent library import decision", fields...)
	return outcome.Inserted, nil
}

func (w *Watcher) importTorrentItem(ctx context.Context, t TorrentInfo, mediaType library.MediaType, sourcePath, destinationPath, title, author, metadataTitle, metadataAuthor, format string, fileSize int64) (bool, error) {
	return w.importTorrentItemWithMetadata(ctx, t, mediaType, sourcePath, destinationPath, title, author, metadataTitle, metadataAuthor, format, fileSize, libraryimport.CandidateMetadata{}, "torrent_name")
}

func (w *Watcher) importTorrentItemWithMetadata(ctx context.Context, t TorrentInfo, mediaType library.MediaType, sourcePath, destinationPath, title, author, metadataTitle, metadataAuthor, format string, fileSize int64, metadataOverride libraryimport.CandidateMetadata, metadataSource string) (bool, error) {
	if w.importer == nil {
		return w.recordTorrentItemWithMetadata(t, string(mediaType), sourcePath, destinationPath, title, author, metadataTitle, metadataAuthor, format, fileSize, metadataSource)
	}

	result, err := w.importer.Import(ctx, libraryimport.ImportRequest{
		Source: library.ImportSource{
			Name:      "torrent",
			SourceID:  t.Hash,
			MediaType: mediaType,
		},
		RootPath:         destinationPath,
		OriginalPath:     sourcePath,
		TitleHint:        title,
		AuthorHint:       author,
		MetadataOverride: metadataOverride,
	})
	fields := []any{
		"torrent_hash", t.Hash,
		"torrent_name", t.Name,
		"source_path", sourcePath,
		"destination_path", destinationPath,
		"file_size", fileSize,
		"detected_format", format,
		"metadata_title", metadataTitle,
		"metadata_author", metadataAuthor,
		"metadata_source", metadataSource,
	}
	if err != nil {
		slog.Error("torrent library import engine failure", append(fields, "error", err)...)
		return false, err
	}
	inserted := result != nil && result.InsertedCount > 0
	slog.Info("torrent library import decision", append(fields,
		"engine", "configured",
		"inserted_count", result.InsertedCount,
		"duplicate_count", result.DuplicateCount,
		"skipped_count", result.SkippedCount,
		"conflict_count", result.ConflictCount,
	)...)
	return inserted, nil
}

func fileFormat(filePath string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(filePath)), ".")
}

// normalizeTorrentPath unescapes HTML entities (e.g. &amp; -> &) that
// qBittorrent may embed in torrent names.
func normalizeTorrentPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return html.UnescapeString(value)
}

// findFilesByExt recursively finds files with given extensions.
func findFilesByExt(root string, exts []string) []string {
	var files []string

	info, err := os.Stat(root)
	if err != nil {
		return files
	}

	if !info.IsDir() {
		lower := strings.ToLower(root)
		for _, ext := range exts {
			if strings.HasSuffix(lower, ext) {
				return []string{root}
			}
		}
		return files
	}

	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		lower := strings.ToLower(path)
		for _, ext := range exts {
			if strings.HasSuffix(lower, ext) {
				files = append(files, path)
				break
			}
		}
		return nil
	}); err != nil {
		slog.Warn("failed to scan torrent files", "root", root, "error", err)
	}

	return files
}
