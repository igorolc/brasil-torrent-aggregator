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

var comando = IndexerMeta{
	Label:       "comando",
	URL:         utils.GetIndexerURLFromEnv("INDEXER_COMANDO_URL", "https://comando.la/"),
	SearchURL:   "?s=",
	PagePattern: "page/%s",
}

var replacer = strings.NewReplacer(
	"janeiro", "01",
	"fevereiro", "02",
	"março", "03",
	"abril", "04",
	"maio", "05",
	"junho", "06",
	"julho", "07",
	"agosto", "08",
	"setembro", "09",
	"outubro", "10",
	"novembro", "11",
	"dezembro", "12",
)

func (i *Indexer) HandlerComandoIndexer(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	metadata := comando

	defer func() {
		i.metrics.IndexerDuration.WithLabelValues(metadata.Label).Observe(time.Since(start).Seconds())
		i.metrics.IndexerRequests.WithLabelValues(metadata.Label).Inc()
	}()

	ctx := r.Context()
	q := r.URL.Query().Get("q")
	page := r.URL.Query().Get("page")

	q = url.QueryEscape(q)
	url := metadata.URL
	if q != "" {
		url = fmt.Sprintf("%s%s%s", url, metadata.SearchURL, q)
	} else if page != "" {
		url = fmt.Sprintf(fmt.Sprintf("%s%s", url, metadata.PagePattern), page)
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
		link, _ := s.Find("h2.entry-title > a").Attr("href")
		links = append(links, link)
	})

	if len(links) == 0 {
		_ = i.requester.ExpireDocument(ctx, url)
	}

	indexedTorrents := utils.ParallelFlatMap(links, func(link string) ([]schema.IndexedTorrent, error) {
		return getTorrents(ctx, i, link, url)
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

func getTorrents(ctx context.Context, i *Indexer, link, referer string) ([]schema.IndexedTorrent, error) {
	var indexedTorrents []schema.IndexedTorrent
	doc, err := getDocument(ctx, i, link, referer)
	if err != nil {
		return nil, err
	}

	article := doc.Find("article")
	title := strings.Replace(article.Find(".entry-title").First().Text(), " - Download", "", -1)
	textContent := article.Find("div.entry-content")
	date := getPublishedDateFromMeta(doc)

	// Extração robusta do Título Original (Nome em Inglês)
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
	article.Find("div.entry-content > p").Each(func(i int, s *goquery.Selection) {
		text := s.Text()
		audio = append(audio, findAudioFromText(text)...)
		y := findYearFromText(text, title)
		if y != "" {
			year = y
		}
		size = append(size, findSizesFromText(text)...)
	})

	imdbLink := ""
	article.Find("a").Each(func(i int, s *goquery.Selection) {
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
				logging.Error().Err(err).Str("magnet_link", magnetLink).Msg("Failed to parse magnet URI")
			}
			releaseTitle := magnet.DisplayName
			infoHash := magnet.InfoHash.String()
			trackers := magnet.Trackers
			magnetAudio := getAudioFromTitle(releaseTitle, audio)

			peer, seed, err := goscrape.GetLeechsAndSeeds(ctx, i.redis, i.metrics, infoHash, trackers)
			if err != nil {
				logging.Error().Err(err).Str("info_hash", infoHash).Msg("Failed to get leechers and seeders")
			}

			// Reconstrução do Título da Release seguindo o Padrão de Ouro
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
				go func() {
					_, _ = i.magnetMetadataAPI.FetchMetadata(ctx, magnetLink)
				}()
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
		indexedTorrents = append(indexedTorrents, it)
	}

	return indexedTorrents, nil
}

func parseLocalizedDate(datePublished string) (time.Time, error) {
	re := regexp.MustCompile(`(\d{1,2}) de (\w+) de (\d{4})`)
	matches := re.FindStringSubmatch(datePublished)
	if len(matches) > 0 {
		day := matches[1]
		if len(day) == 1 {
			day = fmt.Sprintf("0%s", day)
		}
		month := matches[2]
		year := matches[3]
		datePublished = fmt.Sprintf("%s-%s-%s", year, replacer.Replace(month), day)
		date, err := time.Parse("2006-01-02", datePublished)
		if err != nil {
			return time.Time{}, err
		}
		return date, nil
	}
	return time.Time{}, nil
}

func processTitle(title string, a []schema.Audio) string {
	title = strings.Replace(title, " – Download", "", -1)
	title = strings.Replace(title, "comando.la", "", -1)
	title = appendAudioISO639_2Code(title, a)
	return title
}
