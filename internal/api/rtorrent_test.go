package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleTestRTorrentUsesUnsavedValuesAndDoesNotReturnPassword(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "text/xml" {
			t.Fatalf("request = %s %s content-type=%q", r.Method, r.URL, r.Header.Get("Content-Type"))
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "unsaved-user" || password != "unsaved-pass" {
			t.Fatalf("basic auth = %q/%q", user, password)
		}
		io.WriteString(w, `<methodResponse><params><param><value><string>0.9.8</string></value></param></params></methodResponse>`)
	}))
	defer server.Close()
	s, _ := settingsTestServer(t)
	body, _ := json.Marshal(map[string]interface{}{"url": server.URL, "username": "unsaved-user", "password": "unsaved-pass", "timeout_seconds": 2, "allow_private_networks": true})
	rr := httptest.NewRecorder()
	s.handleTestRTorrent(rr, httptest.NewRequest(http.MethodPost, "/api/test/rtorrent", bytes.NewReader(body)))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"success":true`) {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "unsaved-pass") {
		t.Fatal("password leaked in diagnostic response")
	}
}

func TestRTorrentMappingAPIPersistsAndDeletes(t *testing.T) {
	s, _ := settingsTestServer(t)
	body := bytes.NewBufferString(`{"client_id":"rtorrent","remote_path":"/downloads","local_path":"/data/incoming","enabled":true}`)
	rr := httptest.NewRecorder()
	s.handleAddRTorrentMapping(rr, httptest.NewRequest(http.MethodPost, "/api/rtorrent/mappings", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("add status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(s.rtorrentMappings()) != 1 {
		t.Fatalf("mappings=%v", s.rtorrentMappings())
	}
	rr = httptest.NewRecorder()
	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/rtorrent/mappings/0", nil)
	deleteReq.SetPathValue("index", "0")
	s.handleDeleteRTorrentMapping(rr, deleteReq)
	if rr.Code != http.StatusOK || len(s.rtorrentMappings()) != 0 {
		t.Fatalf("delete status=%d mappings=%v", rr.Code, s.rtorrentMappings())
	}
}
