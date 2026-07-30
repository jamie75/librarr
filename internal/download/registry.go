package download

import "fmt"

// ClientRegistry centralizes configured client resolution for future multiple
// client support. Callers receive an explicit error for unknown or read-only
// clients instead of silently falling back to a global setting.
type ClientRegistry struct {
	clients map[string]TorrentClient
}

func NewClientRegistry(clients ...TorrentClient) *ClientRegistry {
	registry := &ClientRegistry{clients: make(map[string]TorrentClient)}
	for _, client := range clients {
		if client == nil {
			continue
		}
		key := client.Name()
		if identified, ok := client.(interface{ ClientID() string }); ok {
			key = identified.ClientID()
		}
		registry.clients[key] = client
	}
	return registry
}

func (r *ClientRegistry) Resolve(clientID string) (TorrentClient, error) {
	if r == nil {
		return nil, fmt.Errorf("download client registry is unavailable")
	}
	client, ok := r.clients[clientID]
	if !ok {
		return nil, fmt.Errorf("download client %q is not configured", clientID)
	}
	return client, nil
}

func (r *ClientRegistry) ResolveWritable(clientID string) (WritableTorrentClient, error) {
	client, err := r.Resolve(clientID)
	if err != nil {
		return nil, err
	}
	writable, ok := client.(WritableTorrentClient)
	if !ok {
		return nil, fmt.Errorf("download client %q does not support torrent submission", clientID)
	}
	return writable, nil
}
