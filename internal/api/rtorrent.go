package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jamie75/librarr/internal/download"
)

type rtorrentRequest struct {
	Enabled    bool   `json:"enabled"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	Timeout    int    `json:"timeout_seconds"`
	LabelField string `json:"label_field"`
	TLSVerify  *bool  `json:"tls_verify"`
}

func (s *Server) handleGetRTorrent(w http.ResponseWriter, _ *http.Request) {
	settings := s.loadSettings()
	get := func(key string, fallback interface{}) interface{} {
		if value, ok := settings[key]; ok {
			return value
		}
		return fallback
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled":           get("rtorrent_enabled", s.cfg.RTorrentEnabled),
		"name":              get("rtorrent_name", s.cfg.RTorrentName),
		"url":               get("rtorrent_url", s.cfg.RTorrentURL),
		"username":          get("rtorrent_user", s.cfg.RTorrentUser),
		"password":          maskedValueIfSet(get("rtorrent_pass", s.cfg.RTorrentPass)),
		"timeout_seconds":   get("rtorrent_timeout_seconds", s.cfg.RTorrentTimeout),
		"label_field":       get("rtorrent_label_field", s.cfg.RTorrentLabelField),
		"tls_verify":        get("rtorrent_tls_verify", s.cfg.RTorrentTLSVerify),
		"client_id":         "rtorrent",
		"client_type":       "rtorrent",
		"write_supported":   true,
		"active":            s.cfg.ActiveTorrentClient() == "rtorrent",
		"cleanup_supported": false,
	})
}

func (s *Server) handleGetDownloadClients(w http.ResponseWriter, _ *http.Request) {
	clients := []map[string]interface{}{
		{"id": "qbittorrent", "type": "qbittorrent", "name": "qBittorrent", "configured": s.cfg.HasQBittorrent(), "writable": s.cfg.HasQBittorrent(), "active": s.cfg.ActiveTorrentClient() == "qbittorrent"},
		{"id": "transmission", "type": "transmission", "name": "Transmission", "configured": s.cfg.HasTransmission(), "writable": s.cfg.HasTransmission(), "active": s.cfg.ActiveTorrentClient() == "transmission"},
		{"id": "rtorrent", "type": "rtorrent", "name": s.cfg.RTorrentName, "configured": s.cfg.HasRTorrent(), "writable": s.cfg.HasRTorrent(), "active": s.cfg.ActiveTorrentClient() == "rtorrent", "cleanup_supported": false},
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"clients": clients})
}

func maskedValueIfSet(value interface{}) string {
	if value == nil {
		return ""
	}
	if strings.TrimSpace(fmt.Sprint(value)) == "" {
		return ""
	}
	return maskedValue
}

func (s *Server) handleTestRTorrent(w http.ResponseWriter, r *http.Request) {
	var payload rtorrentRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && err.Error() != "EOF" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "Invalid JSON"})
		return
	}
	if payload.URL == "" {
		payload.URL = s.cfg.RTorrentURL
	}
	if payload.Name == "" {
		payload.Name = s.cfg.RTorrentName
	}
	if payload.Username == "" {
		payload.Username = s.cfg.RTorrentUser
	}
	if payload.Password == maskedValue {
		payload.Password = s.cfg.RTorrentPass
	}
	if payload.Password == "" {
		payload.Password = s.cfg.RTorrentPass
	}
	if payload.Timeout <= 0 {
		payload.Timeout = s.cfg.RTorrentTimeout
	}
	if payload.Timeout <= 0 {
		payload.Timeout = 10
	}
	tlsVerify := s.cfg.RTorrentTLSVerify
	if payload.TLSVerify != nil {
		tlsVerify = *payload.TLSVerify
	}
	client := download.NewRTorrentClient(download.RTorrentConfig{
		Name: payload.Name, URL: strings.TrimRight(payload.URL, "/"), Username: payload.Username,
		Password: payload.Password, Timeout: time.Duration(payload.Timeout) * time.Second,
		LabelField: payload.LabelField, TLSVerify: tlsVerify,
	})
	result := client.Diagnose(r.Context())
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleListRTorrentDownloads(w http.ResponseWriter, r *http.Request) {
	if s.rtorrent == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{"success": false, "error": "rTorrent client unavailable"})
		return
	}
	items, err := s.rtorrent.ListDownloads(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"downloads": items})
}

func (s *Server) rtorrentMappings() []download.RemotePathMapping {
	settings := s.loadSettings()
	data, ok := settings["rtorrent_path_mappings"]
	if !ok {
		return []download.RemotePathMapping{}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return []download.RemotePathMapping{}
	}
	var mappings []download.RemotePathMapping
	if json.Unmarshal(raw, &mappings) != nil {
		return []download.RemotePathMapping{}
	}
	return mappings
}

func (s *Server) handleGetRTorrentMappings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"mappings": s.rtorrentMappings()})
}

func (s *Server) handleAddRTorrentMapping(w http.ResponseWriter, r *http.Request) {
	var mapping download.RemotePathMapping
	if err := json.NewDecoder(r.Body).Decode(&mapping); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "Invalid JSON"})
		return
	}
	if err := download.ValidateRemotePathMapping(mapping); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	mappings := append(s.rtorrentMappings(), mapping)
	if err := s.saveRTorrentMappings(mappings); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"success": false, "error": "Failed to save mapping"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "mapping": mapping})
}

func (s *Server) handleDeleteRTorrentMapping(w http.ResponseWriter, r *http.Request) {
	index, err := strconv.Atoi(r.PathValue("index"))
	mappings := s.rtorrentMappings()
	if err != nil || index < 0 || index >= len(mappings) {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"success": false, "error": "Mapping not found"})
		return
	}
	mappings = append(mappings[:index], mappings[index+1:]...)
	if err := s.saveRTorrentMappings(mappings); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"success": false, "error": "Failed to save mapping"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *Server) saveRTorrentMappings(mappings []download.RemotePathMapping) error {
	settings := s.loadSettings()
	settings["rtorrent_path_mappings"] = mappings
	body, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.cfg.SettingsFile, body, 0600)
}
