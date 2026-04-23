package api

import (
	"net/http"
	"os"
)

func (h *Handler) fetchHDRTorrent(searchQuery string) ([]schema.IndexedTorrent, error) {
	baseURL := os.Getenv("INDEXER_HDR_TORRENT_URL")
	if baseURL == "" {
		baseURL = "https://hdrtorrent.com"
	}

	config := WPIndexerConfig{
		Name:            "hdr_torrent",
		BaseURL:         baseURL,
		SearchPath:      "/?s=", // HDR Torrent usa ?s= na busca
		ItemSelector:    ".capa-img",
		TitleSelector:   "h2.h6 a",
		LinkSelector:    "h2.h6 a",
		MagnetSelector:  "a[href^='magnet:?xt=']", // Será usado na página de detalhes
	}

	return h.fetchGenericWPIndexer(searchQuery, config)
}
