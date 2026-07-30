package download

import (
	"fmt"
	"path/filepath"
	"strings"
)

// RemotePathMapping translates a path reported by one download client into
// the path visible to Librarr. Mappings are intentionally client-scoped.
type RemotePathMapping struct {
	ClientID    string `json:"client_id"`
	DisplayName string `json:"display_name,omitempty"`
	RemotePath  string `json:"remote_path"`
	LocalPath   string `json:"local_path"`
	Enabled     bool   `json:"enabled"`
}

// PathResolution contains both the result and evidence suitable for logs or
// an administrator preview. A failed resolution never returns a usable path.
type PathResolution struct {
	Strategy      string `json:"strategy"`
	ClientID      string `json:"client_id"`
	ReportedPath  string `json:"reported_path"`
	MatchedRemote string `json:"matched_remote_prefix,omitempty"`
	LocalPrefix   string `json:"local_prefix,omitempty"`
	ResolvedPath  string `json:"resolved_path,omitempty"`
	Exists        bool   `json:"exists"`
	FailureReason string `json:"failure_reason,omitempty"`
}

// ResolveRemotePath applies the longest enabled mapping for clientID. It is
// lexical and does not scan the filesystem; callers may set Exists after a
// separate, policy-approved stat.
func ResolveRemotePath(clientID, reportedPath string, mappings []RemotePathMapping) PathResolution {
	r := PathResolution{Strategy: "unmapped", ClientID: clientID, ReportedPath: reportedPath}
	reported := cleanPath(reportedPath)
	if reported == "" {
		r.Strategy = "rejected"
		r.FailureReason = "reported path is empty"
		return r
	}

	var best *RemotePathMapping
	bestRemote := ""
	for i := range mappings {
		m := &mappings[i]
		if !m.Enabled || !strings.EqualFold(strings.TrimSpace(m.ClientID), strings.TrimSpace(clientID)) {
			continue
		}
		remote := cleanPath(m.RemotePath)
		local := cleanPath(m.LocalPath)
		if remote == "" || local == "" || !pathWithin(reported, remote) {
			continue
		}
		if len(remote) > len(bestRemote) {
			best, bestRemote = m, remote
		}
	}
	if best == nil {
		r.FailureReason = "no enabled mapping matched the client and path"
		return r
	}

	local := cleanPath(best.LocalPath)
	remaining, err := filepath.Rel(bestRemote, reported)
	if err != nil || remaining == ".." || strings.HasPrefix(remaining, ".."+string(filepath.Separator)) {
		r.Strategy = "rejected"
		r.FailureReason = "reported path escaped the remote prefix"
		return r
	}
	resolved := filepath.Clean(filepath.Join(local, remaining))
	if !pathWithin(resolved, local) {
		r.Strategy = "rejected"
		r.FailureReason = "resolved path escaped the local prefix"
		return r
	}
	r.Strategy = "mapped"
	r.MatchedRemote = bestRemote
	r.LocalPrefix = local
	r.ResolvedPath = resolved
	return r
}

func cleanPath(raw string) string {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if raw == "" {
		return ""
	}
	cleaned := filepath.Clean(filepath.FromSlash(raw))
	if cleaned == "." || strings.ContainsRune(cleaned, 0) {
		return ""
	}
	return cleaned
}

func pathWithin(candidate, root string) bool {
	if candidate == root {
		return true
	}
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// ValidateRemotePathMapping validates an entry before persistence.
func ValidateRemotePathMapping(m RemotePathMapping) error {
	if strings.TrimSpace(m.ClientID) == "" {
		return fmt.Errorf("client_id is required")
	}
	if cleanPath(m.RemotePath) == "" || cleanPath(m.LocalPath) == "" {
		return fmt.Errorf("remote_path and local_path are required")
	}
	return nil
}
