package download

import "testing"

func TestResolveRemotePathUsesLongestClientScopedPrefix(t *testing.T) {
	mappings := []RemotePathMapping{
		{ClientID: "qbittorrent", RemotePath: "/downloads", LocalPath: "/data/incoming", Enabled: true},
		{ClientID: "rtorrent", RemotePath: "/downloads", LocalPath: "/mnt/seedbox", Enabled: true},
		{ClientID: "rtorrent", RemotePath: "/downloads/rclone-mnt/downloads", LocalPath: "/data/incoming", Enabled: true},
	}
	r := ResolveRemotePath("rtorrent", "/downloads/rclone-mnt/downloads/example.epub", mappings)
	if r.Strategy != "mapped" || r.ResolvedPath != "/data/incoming/example.epub" {
		t.Fatalf("resolution = %+v", r)
	}
}

func TestResolveRemotePathRejectsBoundaryTraversalAndDisabledMappings(t *testing.T) {
	mapping := RemotePathMapping{ClientID: "rtorrent", RemotePath: "/downloads", LocalPath: "/data/incoming", Enabled: true}
	if got := ResolveRemotePath("rtorrent", "/downloads-other/file.epub", []RemotePathMapping{mapping}); got.Strategy != "unmapped" {
		t.Fatalf("prefix boundary unexpectedly matched: %+v", got)
	}
	mapping.Enabled = false
	if got := ResolveRemotePath("rtorrent", "/downloads/file.epub", []RemotePathMapping{mapping}); got.Strategy != "unmapped" {
		t.Fatalf("disabled mapping unexpectedly matched: %+v", got)
	}
	if got := ResolveRemotePath("rtorrent", "/downloads/../secret.epub", []RemotePathMapping{mapping}); got.Strategy != "unmapped" {
		t.Fatalf("traversal path unexpectedly matched: %+v", got)
	}
}

func TestResolveRemotePathAcceptsWindowsSeparators(t *testing.T) {
	r := ResolveRemotePath("rtorrent", `C:\downloads\book.epub`, []RemotePathMapping{{
		ClientID: "rtorrent", RemotePath: `C:\downloads`, LocalPath: `/data/incoming`, Enabled: true,
	}})
	if r.Strategy != "mapped" || r.ResolvedPath != "/data/incoming/book.epub" {
		t.Fatalf("resolution = %+v", r)
	}
}
