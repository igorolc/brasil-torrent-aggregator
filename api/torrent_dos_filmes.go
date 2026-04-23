package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/felipemarinho97/torrent-indexer/logging"
	"github.com/felipemarinho97/torrent-indexer/magnet"
	"github.com/felipemarinho97/torrent-indexer/schema"
	goscrape "github.com/felipemarinho97/torrent-indexer/scrape"
	"github.com/felipemarinho97/torrent-indexer/utils"
)

var torrentDosFilmes = IndexerMeta{
	Label:       "torrent_dos_filmes",
	URL:         utils.GetIndexerURLFromEnv("INDEXER_TORRENT_DOS_FILMES_URL", "https://torrentdosfilmes.net/"),
	SearchURL:   "?s=",
	PagePattern: "page/%s",
}

func (i *Indexer) HandlerTorrentDosFilmesIndexer(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	metadata := torrentDosFilmes

	defer func() {
		i.metrics.IndexerDuration.WithLabelValues(metadata.Label).Observe(time.Since(start).Seconds())
		i.metrics.IndexerRequests.WithLabelValues(metadata.Label).Inc()
	}()

	ctx := r.Context()
	q := r.URL.Query().Get("q")
	page := r.URL.Query().Get("page")

	q = url.QueryEscape(q)
	url := metadata.URL
	if page != "" {
		url = fmt.Sprintf(fmt.Sprintf("%s%s", url, metadata.PagePattern), page)
	} else {
		url = fmt.Sprintf("%s%s%s", url, metadata.SearchURL, q)
	}

	logging.InfoWithRequest(r).Str("target_url", url).Msg("Processing indexer request")
	resp, err := i.requester.GetDocument(ctx, url)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		i.metrics.IndexerErrors.WithLabelValues(metadata.Label).Inc()
		return
	}
	defer resp.Close()

	doc, err := goquery.NewDocumentFromReader(resp)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		i.metrics.IndexerErrors.WithLabelValues(metadata.Label).Inc()
		return
	}

	var links []string
	doc.Find("article").Each(func(i int, s *goquery.Selection) {
		link, _ := s.Find("h2 > a").Attr("href")
		links = append(links, link)
	})

	if len(links) == 0 {
		_ = i.requester.ExpireDocument(ctx, url)
	}

	indexedTorrents := utils.ParallelFlatMap(links, func(link string) ([]schema.IndexedTorrent, error) {
		return getTorrentsTorrentDosFilmes(ctx, i, link, url)
	})

	postProcessedTorrents := indexedTorrents
	for _, processor := range i.postProcessors {
		postProcessedTorrents = processor(i, r, postProcessedTorrents)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(Response{
		Results:      postProcessedTorrents,
		Count:        len(postProcessedTorrents),
		IndexedCount: len(indexedTorrents),
	})
}

func getTorrentsTorrentDosFilmes(ctx context.Context, i *Indexer, link, referer string) ([]schema.IndexedTorrent, error) {
	var indexedTorrents []schema.IndexedTorrent
	doc, err := getDocument(ctx, i, link, referer)
	if err != nil {
		return nil, err
	}

	article := doc.Find("article")
	// Clean site labels
	title := strings.TrimSpace(article.Find("h1").First().Text())
	title = strings.ReplaceAll(title, "Torrent dos Filmes", "")
	title = strings.ReplaceAll(title, "TorrentDosFilmes", "")
	title = strings.TrimSpace(title)

	textContent := article.Find(".post-content")
	date := getPublishedDateFromMeta(doc)

	// Extração do Título Original (Nome em Inglês)
	originalTitleEng := ""
	fullTxt := article.Text()
	match := regexp.MustCompile(`(?i)(?:Título|Titulo|Original)\s*Original\s*:\s*([^·\n\t\r<|/]+)`).FindStringSubmatch(fullTxt)
	if len(match) > 1 {
		originalTitleEng = utils.CleanOriginalTitle(match[1])
	}

	magnets := textContent.Find("a[href^=\"magnet\"]")
	var magnetLinks []string
	magnets.Each(func(i int, s *goquery.Selection) {
		magnetLink, _ := s.Attr("href")
		magnetLinks = append(magnetLinks, magnetLink)
	})

	var audio []schema.Audio
	var year string
	var size []string
	article.Find(".post-content p").Each(func(i int, s *goquery.Selection) {
		text := s.Text()
		audio = append(audio, findAudioFromText(text)...)
		y := findYearFromText(text, title)
		if y != "" {
			year = y
		}
		size = append(size, findSizesFromText(text)...)
	})

	imdbLink := ""
	article.Find(".post-content a").Each(func(i int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		_imdbLink, err := getIMDBLink(href)
		if err == nil {
			imdbLink = _imdbLink
		}
	})

	size = utils.StableUniq(size)
	var chanIndexedTorrent = make(chan schema.IndexedTorrent)

	for it, magnetLink := range magnetLinks {
		it := it
		go func(it int, magnetLink string) {
			magnet, err := magnet.ParseMagnetUri(magnetLink)
			if err != nil {
				chanIndexedTorrent <- schema.IndexedTorrent{}
				return
			}
			releaseTitle := magnet.DisplayName
			infoHash := magnet.InfoHash.String()
			trackers := magnet.Trackers
			magnetAudio := getAudioFromTitle(releaseTitle, audio)

			peer, seed, _ := goscrape.GetLeechsAndSeeds(ctx, i.redis, i.metrics, infoHash, trackers)

			// Padrão de Ouro para Título da Release
			quality := findQualityFromTitle(releaseTitle)
			if quality == "" {
				quality = findQualityFromText(article.Text())
			}

			finalOriginalTitle := originalTitleEng
			if finalOriginalTitle == "" {
				finalOriginalTitle = utils.CleanTitle(title)
			}

			displayTitle := fmt.Sprintf("%s %s %s %s", finalOriginalTitle, year, quality, strings.Join(schema.AudioToString(magnetAudio), " "))
			displayTitle = strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(displayTitle, " "))

			var mySize string
			if len(size) == len(magnetLinks) {
				mySize = size[it]
			}
			if mySize == "" {
				go func() { _, _ = i.magnetMetadataAPI.FetchMetadata(ctx, magnetLink) }()
			}

			ixt := schema.IndexedTorrent{
				Title:         displayTitle,
				OriginalTitle: finalOriginalTitle,
				Details:       link,
				Year:          year,
				IMDB:          imdbLink,
				Audio:         magnetAudio,
				MagnetLink:    magnetLink,
				Date:          date,
				InfoHash:      infoHash,
				Trackers:      trackers,
				LeechCount:    peer,
				SeedCount:     seed,
				Size:          mySize,
			}
			chanIndexedTorrent <- ixt
		}(it, magnetLink)
	}

	for i := 0; i < len(magnetLinks); i++ {
		it := <-chanIndexedTorrent
		if it.InfoHash != "" {
			indexedTorrents = append(indexedTorrents, it)
		}
	}

	return indexedTorrents, nil
}
