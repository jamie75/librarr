package download

import "github.com/jamie75/librarr/internal/config"

// TorrentSubmission is the stable identity returned by a write-capable
// client. It is persisted before polling begins.
type TorrentSubmission struct {
	ClientID       string
	ClientType     string
	DownloadID     string
	InfoHash       string
	Name           string
	RemoteSavePath string
	Category       string
}

type TorrentSubmissionRequest struct {
	URL              string
	TorrentBytes     []byte
	Filename         string
	Title            string
	SavePath         string
	Category         string
	ExpectedInfoHash string
	AddStopped       bool // rTorrent-only compatibility option; submissions start by default.
}

// WritableTorrentClient is optional so existing TorrentClient implementations
// and test doubles remain source-compatible while clients gain stable results.
type WritableTorrentClient interface {
	SubmitTorrent(TorrentSubmissionRequest) (TorrentSubmission, error)
}

// TorrentClient is the common interface implemented by every torrent download
// backend (qBittorrent, Transmission). The rest of Librarr — the download
// Manager and the completion Watcher — talks to torrents exclusively through
// this interface so the active backend is a single configuration choice rather
// than a hard dependency.
//
// All implementations report torrent state using the qBittorrent state
// vocabulary so callers can keep using MapTorrentStatus uniformly. Listing and
// deletion are scoped to torrents Librarr itself added (qBittorrent categories,
// Transmission labels), so Librarr never touches a user's unrelated torrents.
type TorrentClient interface {
	// AddTorrent submits a torrent URL, torrent file URL, or magnet link.
	// savePath and category fall back to the client's configured defaults when
	// empty. expectedInfoHash is optional and used for post-add verification.
	AddTorrent(torrentURL, title, savePath, category, expectedInfoHash string) error
	// GetTorrents returns torrents Librarr added under the given category
	// (empty category returns all Librarr-scoped torrents).
	GetTorrents(category string) ([]TorrentInfo, error)
	// GetTorrentFiles lists the files contained in a torrent by hash.
	GetTorrentFiles(hash string) ([]TorrentFile, error)
	// DeleteTorrent removes a torrent by hash, optionally deleting its data.
	DeleteTorrent(hash string, deleteFiles bool) error
	// Name is the lowercase client identifier, e.g. "qbittorrent".
	Name() string
}

// Compile-time assertions that both backends satisfy the interface.
var (
	_ TorrentClient = (*QBittorrentClient)(nil)
	_ TorrentClient = (*TransmissionClient)(nil)
	_ TorrentClient = (*RTorrentClient)(nil)
)

// SelectTorrentClient returns the active torrent backend per the configuration,
// or nil when no torrent client is configured. The selection rule lives in
// config.ActiveTorrentClient (explicit TORRENT_CLIENT override, else
// qBittorrent-preferred auto-detect).
func SelectTorrentClient(cfg *config.Config, qb *QBittorrentClient, tr *TransmissionClient) TorrentClient {
	return SelectTorrentClientWithRTorrent(cfg, qb, tr, nil)
}

func SelectTorrentClientWithRTorrent(cfg *config.Config, qb *QBittorrentClient, tr *TransmissionClient, rt *RTorrentClient) TorrentClient {
	switch cfg.ActiveTorrentClient() {
	case "qbittorrent":
		return qb
	case "transmission":
		return tr
	case "rtorrent":
		return rt
	default:
		return nil
	}
}
