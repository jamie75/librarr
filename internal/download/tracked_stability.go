package download

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// localContentObservation is deliberately in-memory: waiting_for_sync is the
// durable state, while a restart simply requires one additional safe poll
// before import rather than trusting an observation made before the restart.
type localContentObservation struct {
	signature string
}

// localContentIsStable requires the same relevant content snapshot on two
// watcher polls. This prevents imports while rclone is still copying a file or
// directory, without treating expected synchronization lag as a failure.
func (w *Watcher) localContentIsStable(id, root, mediaType string) (bool, string) {
	signature, err := localContentSignature(root, mediaType)
	if err != nil {
		w.stability.Delete(id)
		return false, localContentAvailabilityReason(err)
	}
	if previous, ok := w.stability.Load(id); !ok || previous.(localContentObservation).signature != signature {
		w.stability.Store(id, localContentObservation{signature: signature})
		return false, "waiting for local content to become stable"
	}
	w.stability.Delete(id)
	return true, ""
}

func localContentSignature(root, mediaType string) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		if isTemporaryTorrentFile(info.Name()) {
			return "", fmt.Errorf("local content is still temporary")
		}
		if !isSupportedTrackedMedia(root, mediaType) {
			return "", fmt.Errorf("completed content has unsupported format")
		}
		return fmt.Sprintf("file:%d:%d", info.Size(), info.ModTime().UnixNano()), nil
	}

	var files, total int64
	var newest int64
	var temporary bool
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if isTemporaryTorrentFile(entry.Name()) {
			temporary = true
			return nil
		}
		if !isSupportedTrackedMedia(path, mediaType) {
			return nil
		}
		fileInfo, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		files++
		total += fileInfo.Size()
		if modified := fileInfo.ModTime().UnixNano(); modified > newest {
			newest = modified
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if temporary {
		return "", fmt.Errorf("local content still contains temporary files")
	}
	if files == 0 {
		return "", fmt.Errorf("completed content has no supported %s files", mediaType)
	}
	return fmt.Sprintf("dir:%d:%d:%d", files, total, newest), nil
}

func isSupportedTrackedMedia(path, mediaType string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch mediaType {
	case "audiobook":
		return ext == ".m4b" || ext == ".mp3" || ext == ".m4a"
	case "manga":
		return ext == ".cbz" || ext == ".cbr" || ext == ".zip" || ext == ".pdf" || ext == ".epub"
	default:
		return ext == ".epub" || ext == ".mobi" || ext == ".pdf" || ext == ".azw3"
	}
}

func isTemporaryTorrentFile(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return strings.HasSuffix(lower, ".part") || strings.HasSuffix(lower, ".partial") ||
		strings.HasSuffix(lower, ".tmp") || strings.HasSuffix(lower, ".crdownload") ||
		strings.HasSuffix(lower, ".!qb")
}

func localContentAvailabilityReason(err error) string {
	if err == nil {
		return ""
	}
	if os.IsNotExist(err) {
		return "waiting for local content"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "stale file handle") || strings.Contains(message, "resource temporarily unavailable") || strings.Contains(message, "input/output error") {
		return "local mount is temporarily unavailable"
	}
	return "waiting for local content"
}

func retryableTorrentPollError(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(strings.ToLower(err.Error()), "context deadline exceeded") {
		return "temporary rTorrent connection error: request timed out; retrying"
	}
	return "temporary rTorrent connection error; retrying"
}
