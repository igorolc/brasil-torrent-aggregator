package handler

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/felipemarinho97/torrent-indexer/logging"
	"github.com/felipemarinho97/torrent-indexer/schema"
	"github.com/felipemarinho97/torrent-indexer/utils"
)

// Torznab Response Structures
type TorznabRSS struct {
	XMLName xml.Name       `xml:"rss"`
	Version string         `xml:"version,attr"`
	Torznab string         `xml:"xmlns:torznab,attr"`
	Channel TorznabChannel `xml:"channel"`
}

type TorznabChannel struct {
	Title       string        `xml:"title"`
	Description string        `xml:"description"`
	Link        string        `xml:"link"`
	Language    string        `xml:"language"`
	Category    []TorznabCaps `xml:"category,omitempty"`
	Items       []TorznabItem `xml:"item"`
}

type TorznabItem struct {
	Title          string        `xml:"title"`
	Guid           string        `xml:"guid"`
	Link           string        `xml:"link"`
	Comments       string        `xml:"comments,omitempty"`
	PubDate        string        `xml:"pubDate"`
	Enclosure      TorznabEncl   `xml:"enclosure"`
	TorznabAttributes []TorznabAttr `xml:"torznab:attr"`
}

type TorznabEncl struct {
	URL    string `xml:"url,attr"`
	Length string `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type TorznabAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type TorznabCaps struct {
	ID   int    `xml:"id,attr"`
	Name string `xml:"name,attr"`
}

func (i *Indexer) HandlerTorznab(w http.ResponseWriter, r *http.Request) {
	indexerName := strings.TrimPrefix(r.URL.Path, "/indexers/")
	indexerName = strings.TrimSuffix(indexerName, "/api")

	t := r.URL.Query().Get("t")

	switch t {
	case "caps":
		i.handleCaps(w, r)
	case "search", "movie", "movie-search":
		i.handleSearch(w, r, indexerName)
	default:
		i.handleCaps(w, r)
	}
}

func (i *Indexer) handleCaps(w http.ResponseWriter, r *http.Request) {
	caps := `<caps>
		<server version="1.0" title="Torrent Indexer" />
		<limits max="100" default="20" />
		<registration status="open" memory="yes" />
		<searching>
			<search available="yes" supportedParams="q" />
			<movie-search available="yes" supportedParams="q,imdbid" />
		</searching>
		<categories>
			<category id="2000" name="Movies">
				<subcat id="2010" name="Foreign" />
				<subcat id="2030" name="1080p" />
				<subcat id="2040" name="HD" />
			</category>
		</categories>
	</caps>`
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(caps))
}

func (i *Indexer) handleSearch(w http.ResponseWriter, r *http.Request, indexerName string) {
	q := r.URL.Query().Get("q")
	if q == "" {
		q = r.URL.Query().Get("imdbid")
	}
	page := r.URL.Query().Get("offset")

	var results []schema.IndexedTorrent
	var err error

	// Suporte para indexadores
	switch indexerName {
	case "brasil_aggregator":
		results, err = i.fetchBrasilAggregator(r.Context(), q, page)
	case "rede_torrent":
		results, err = i.fetchGenericWPIndexer(r.Context(), q, page, rede_torrent)
	case "sem_torrent":
		sem_meta := IndexerMeta{
			Label:       "sem_torrent",
			URL:         utils.GetIndexerURLFromEnv("INDEXER_SEM_TORRENT_URL", "https://semtorrent.com/"),
			SearchURL:   "index.php?s=",
			PagePattern: "%s",
		}
		results, err = i.fetchGenericWPIndexer(r.Context(), q, page, sem_meta)
	case "vaca_torrent":
		results, err = i.fetchVacaTorrent(r.Context(), q, page)
	default:
		http.Error(w, "Indexer not supported for Torznab yet", http.StatusNotFound)
		return
	}

	if err != nil {
		logging.ErrorWithRequest(r).Err(err).Str("indexer", indexerName).Msg("Torznab search failed")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Aplicar Pós-processadores (Importante para o Padrão de Ouro)
	for _, processor := range i.postProcessors {
		results = processor(i, r, results)
	}

	rss := TorznabRSS{
		Version: "2.0",
		Torznab: "http://torznab.com/schemas/2015/feed",
		Channel: TorznabChannel{
			Title:       fmt.Sprintf("%s - Torznab", indexerName),
			Description: "Torrent Indexer Torznab API",
			Link:        r.URL.String(),
			Language:    "pt-br",
			Items:       make([]TorznabItem, 0, len(results)),
		},
	}

	for _, res := range results {
		category := getCategoryFromTitle(res.Title)
		attrs := []TorznabAttr{
			{Name: "category", Value: category},
			{Name: "seeders", Value: strconv.Itoa(res.SeedCount)},
			{Name: "peers", Value: strconv.Itoa(res.LeechCount)},
			{Name: "size", Value: strconv.FormatInt(res.SizeInBytes, 10)},
			{Name: "infohash", Value: res.InfoHash},
		}

		titleUpper := strings.ToUpper(res.Title)
		if strings.Contains(titleUpper, "DUBLADO") || strings.Contains(titleUpper, "DUAL") || strings.Contains(titleUpper, "PORTUGUES") {
			attrs = append(attrs, TorznabAttr{Name: "language", Value: "Portuguese"})
		} else if strings.Contains(titleUpper, "LEGENDADO") {
			attrs = append(attrs, TorznabAttr{Name: "language", Value: "English"})
		}

		if res.IMDB != "" {
			attrs = append(attrs, TorznabAttr{Name: "imdb", Value: res.IMDB})
		}

		item := TorznabItem{
			Title:   res.Title,
			Guid:    res.InfoHash,
			Link:    res.MagnetLink,
			PubDate: res.Date.Format(time.RFC1123Z),
			Enclosure: TorznabEncl{
				URL:    res.MagnetLink,
				Length: strconv.FormatInt(res.SizeInBytes, 10),
				Type:   "application/x-bittorrent",
			},
			TorznabAttributes: attrs,
		}
		rss.Channel.Items = append(rss.Channel.Items, item)
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_ = xml.NewEncoder(w).Encode(rss)
}
func getCategoryFromTitle(title string) string {
	title = strings.ToUpper(title)
	if strings.Contains(title, "2160P") || strings.Contains(title, "4K") || strings.Contains(title, "ULTRA HD") {
		return "2045" // Movies/4K
	}
	if strings.Contains(title, "1080P") {
		return "2040" // Movies/HD
	}
	if strings.Contains(title, "720P") {
		return "2030" // Movies/SD (alguns mapeiam como HD, mas Radarr entende)
	}
	return "2000" // Movies (Generic)
}
