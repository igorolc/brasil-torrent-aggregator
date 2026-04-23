package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/felipemarinho97/torrent-indexer/logging"
	"github.com/felipemarinho97/torrent-indexer/schema"
	"github.com/felipemarinho97/torrent-indexer/utils"
)

func (i *Indexer) HandlerBrasilAggregator(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	label := "brasil_aggregator"

	defer func() {
		i.metrics.IndexerDuration.WithLabelValues(label).Observe(time.Since(start).Seconds())
		i.metrics.IndexerRequests.WithLabelValues(label).Inc()
	}()

	q := r.URL.Query().Get("q")
	if q == "" {
		q = r.URL.Query().Get("query")
	}
	page := r.URL.Query().Get("page")

	finalResults, err := i.fetchBrasilAggregator(r.Context(), q, page)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(Response{
		Results:      finalResults,
		Count:        len(finalResults),
		IndexedCount: len(finalResults),
	})
}

func (i *Indexer) fetchBrasilAggregator(ctx context.Context, q, page string) ([]schema.IndexedTorrent, error) {
	logging.Info().Str("query", q).Msg("Brasil Aggregator started fetch")

	indexers := []IndexerMeta{
		rede_torrent,
		{
			Label:       "sem_torrent",
			URL:         utils.GetIndexerURLFromEnv("INDEXER_SEM_TORRENT_URL", "https://semtorrent.com/"),
			SearchURL:   "index.php?s=",
			PagePattern: "%s",
		},
		{
			Label:       "hdr_torrent",
			URL:         utils.GetIndexerURLFromEnv("INDEXER_HDR_TORRENT_URL", "https://hdrtorrent.com/"),
			SearchURL:   "?s=",
			PagePattern: "%s",
			ListSelector: ".capa-img",
			ContainerSelector: "main",
		},
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	allTorrents := make(map[string]schema.IndexedTorrent)

	for _, meta := range indexers {
		wg.Add(1)
		go func(m IndexerMeta) {
			defer wg.Done()
			results, err := i.fetchGenericWPIndexer(ctx, q, page, m)
			if err != nil {
				return
			}

			mu.Lock()
			defer mu.Unlock()
			for _, t := range results {
				infoHash := strings.ToLower(t.InfoHash)
				if infoHash == "" {
					continue
				}
				if existing, ok := allTorrents[infoHash]; ok {
					if !strings.Contains(existing.Indexer, m.Label) {
						existing.Indexer = fmt.Sprintf("%s | %s", existing.Indexer, m.Label)
					}
					existing.Trackers = utils.StableUniq(append(existing.Trackers, t.Trackers...))
					if t.Seeders > existing.Seeders {
						existing.Seeders = t.Seeders
						existing.SeedCount = t.Seeders
					}
					allTorrents[infoHash] = existing
				} else {
					allTorrents[infoHash] = t
				}
			}
		}(meta)
	}

	wg.Wait()

	var finalResults []schema.IndexedTorrent
	for _, t := range allTorrents {
		sources := strings.Split(t.Indexer, " | ")
		var shortSources []string
		for _, s := range sources {
			switch s {
			case "rede_torrent":
				shortSources = append(shortSources, "RT")
			case "sem_torrent":
				shortSources = append(shortSources, "ST")
			case "hdr_torrent":
				shortSources = append(shortSources, "HT")
			default:
				shortSources = append(shortSources, strings.ToUpper(s[:2]))
			}
		}
		
		sourceTag := fmt.Sprintf("[%s]", strings.Join(shortSources, "|"))
		t.Title = fmt.Sprintf("%s %s", t.Title, sourceTag)
		finalResults = append(finalResults, t)
	}

	logging.Info().Int("total_unique", len(finalResults)).Msg("Aggregator fetch finished")
	return finalResults, nil
}
