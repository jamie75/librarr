package download

import "testing"

func TestClientRegistryResolvesStableClientID(t *testing.T) {
	client := NewRTorrentClient(RTorrentConfig{Name: "Seedbox rTorrent"})
	registry := NewClientRegistry(client)
	got, err := registry.Resolve("rtorrent")
	if err != nil || got != client {
		t.Fatalf("Resolve = %v, %v", got, err)
	}
	if _, err := registry.ResolveWritable("missing"); err == nil {
		t.Fatal("expected unknown client error")
	}
}
