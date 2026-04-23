package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/felipemarinho97/torrent-indexer/magnet"
	"github.com/felipemarinho97/torrent-indexer/schema"
	goscrape "github.com/felipemarinho97/torrent-indexer/scrape"
	"github.com/felipemarinho97/torrent-indexer/utils"
)

var vacaTorrent = IndexerMeta{
	Label:       "vaca_torrent",
	URL:         utils.GetIndexerURLFromEnv("INDEXER_VACA_TORRENT_URL", "https://vacatorrentmov.com/"),
	SearchURL:   "wp-admin/admin-ajax.php",
	PagePattern: "page/%s",
}

func (i *Indexer) HandlerVacaTorrentIndexer(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	metadata := vacaTorrent

	defer func() {
		i.metrics.IndexerDuration.WithLabelValues(metadata.Label).Observe(time.Since(start).Seconds())
		i.metrics.IndexerRequests.WithLabelValues(metadata.Label).Inc()
	}()

	q := r.URL.Query().Get("q")
	page := r.URL.Query().Get("page")

	indexedTorrents, err := i.fetchVacaTorrent(r.Context(), q, page)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		i.metrics.IndexerErrors.WithLabelValues(metadata.Label).Inc()
		return
	}

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

func (i *Indexer) fetchVacaTorrent(ctx context.Context, q, page string) ([]schema.IndexedTorrent, error) {
	metadata := vacaTorrent
	if page == "" {
		page = "1"
	}

	var doc *goquery.Document
	var err error
	var targetURL string

	if q != "" {
		searchQ := CleanSearchQuery(q)
		targetURL = fmt.Sprintf("%s%s", metadata.URL, metadata.SearchURL)
		doc, err = postSearchVacaTorrent(ctx, i, targetURL, searchQ, page)
		if err != nil {
			return nil, err
		}
	} else {
		targetURL = metadata.URL
		if page != "" && page != "1" {
			targetURL = fmt.Sprintf(fmt.Sprintf("%s%s", targetURL, metadata.PagePattern), page)
		}

		resp, err := i.requester.GetDocument(ctx, targetURL)
		if err != nil {
			return nil, err
		}
		defer resp.Close()

		doc, err = goquery.NewDocumentFromReader(resp)
		if err != nil {
			return nil, err
		}
	}

	var links []string
	selector := ".i-tem_ht"
	doc.Find(selector).Each(func(_ int, s *goquery.Selection) {
		link, exists := s.Find("a").Attr("href")
		if exists {
			links = append(links, link)
		}
	})

	if len(links) == 0 {
		_ = i.requester.ExpireDocument(ctx, targetURL)
	}

	soraFetcher, err := utils.NewSoraLinkFetcher("https://vacadb.org", i.redis)
	if err != nil {
		return nil, err
	}

	indexedTorrents := utils.ParallelFlatMap(links, func(link string) ([]schema.IndexedTorrent, error) {
		return getTorrentsVacaTorrent(ctx, i, link, targetURL, soraFetcher)
	})

	return indexedTorrents, nil
}

func postSearchVacaTorrent(ctx context.Context, i *Indexer, targetURL, query, page string) (*goquery.Document, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	_ = writer.WriteField("action", "filtrar_busca")
	_ = writer.WriteField("s", query)
	_ = writer.WriteField("tipo", "todos")
	_ = writer.WriteField("paged", page)

	_ = writer.Close()

	req, err := http.NewRequestWithContext(ctx, "POST", targetURL, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:144.0) Gecko/20100101 Firefox/144.0")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", "https://vacatorrentmov.com")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	var ajaxResp struct {
		HTML string `json:"html"`
	}
	_ = json.Unmarshal(bodyBytes, &ajaxResp)

	unescapedHTML := html.UnescapeString(ajaxResp.HTML)
	return goquery.NewDocumentFromReader(strings.NewReader(unescapedHTML))
}

var commentRegex = regexp.MustCompile(`<!--(.*?)-->`)

func getTorrentsVacaTorrent(ctx context.Context, i *Indexer, link, referer string, soraFetcher *utils.SoraLinkFetcher) ([]schema.IndexedTorrent, error) {
	var indexedTorrents []schema.IndexedTorrent
	doc, err := getDocument(ctx, i, link, referer)
	if err != nil {
		return nil, err
	}

	title := strings.TrimSpace(doc.Find(".custom-main-title").First().Text())
	if title == "" {
		title = strings.TrimSpace(doc.Find("h1").First().Text())
	}
	title = strings.TrimSpace(strings.Split(title, "(")[0])

	var year string
	var imdbLink string
	var audio []schema.Audio
	var size []string
	var season string
	date := getPublishedDateFromMeta(doc)

	// Extração do Título Original (Nome em Inglês)
	originalTitleEng := ""
	fullTxt := doc.Find("body").Text()
	match := regexp.MustCompile(`(?i)(?:Título|Titulo|Original)\s*Original\s*:\s*([^·\n\t\r<|/]+)`).FindStringSubmatch(fullTxt)
	if len(match) > 1 {
		originalTitleEng = utils.CleanOriginalTitle(match[1])
	}

	doc.Find(".col-left ul li, .content p").Each(func(_ int, s *goquery.Selection) {
		text := s.Text()
		htmlContent, _ := s.Html()

		if year == "" {
			year = findYearFromText(text, title)
		}

		if imdbLink == "" {
			s.Find("a").Each(func(_ int, linkItem *goquery.Selection) {
				href, exists := linkItem.Attr("href")
				if exists && strings.Contains(href, "imdb.com") {
					_imdbLink, err := getIMDBLink(href)
					if err == nil {
						imdbLink = _imdbLink
					}
				}
			})
		}

		audio = append(audio, findAudioFromText(text)...)

		if strings.Contains(text, "Season:") || strings.Contains(text, "Temporada:") {
			seasonMatch := regexp.MustCompile(`(\d+)`).FindStringSubmatch(text)
			if len(seasonMatch) > 0 {
				season = seasonMatch[1]
			}
		}

		if date.IsZero() {
			date = getPublishedDateFromRawString(text)
		}
		size = append(size, findSizesFromText(htmlContent)...)
	})

	if date.Year() == 0 && year != "" {
		yearInt, _ := strconv.Atoi(year)
		date = time.Date(yearInt, 1, 1, 0, 0, 0, 0, time.UTC)
	}

	var magnetLinks []string
	doc.Find("a[href^=\"magnet\"]").Each(func(_ int, s *goquery.Selection) {
		magnetLink, _ := s.Attr("href")
		magnetLinks = append(magnetLinks, magnetLink)
	})

	doc.Find(".area-links-download a").Each(func(_ int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if exists && strings.Contains(href, "vacadb.org") {
			u, err := url.Parse(href)
			if err == nil {
				queryID := u.Query().Get("id")
				if queryID != "" {
					magnetLink, err := soraFetcher.FetchLink(ctx, queryID)
					if err == nil && magnetLink != "" {
						magnetLinks = append(magnetLinks, magnetLink)
					}
				}
			}
		}
	})

	doc.Find(".streaming-container").Each(func(_ int, s *goquery.Selection) {
		htmlContent, _ := s.Html()
		htmlContent = strings.ReplaceAll(htmlContent, "\n", "")
		matches := commentRegex.FindAllStringSubmatch(htmlContent, -1)
		for _, m := range matches {
			if strings.HasPrefix(strings.TrimSpace(m[1]), "<") {
				commentDoc, err := goquery.NewDocumentFromReader(strings.NewReader(m[1]))
				if err == nil {
					commentDoc.Find("a[href^=\"magnet\"]").Each(func(_ int, linkItem *goquery.Selection) {
						ml, _ := linkItem.Attr("href")
						magnetLinks = append(magnetLinks, ml)
					})
				}
			}
		}
	})

	size = utils.StableUniq(size)
	var chanIndexedTorrent = make(chan schema.IndexedTorrent, len(magnetLinks))
	var wg sync.WaitGroup

	for it, magnetLink := range magnetLinks {
		wg.Add(1)
		it := it
		go func(it int, magnetLink string) {
			defer wg.Done()
			magnet, err := magnet.ParseMagnetUri(magnetLink)
			if err != nil {
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
				quality = findQualityFromText(fullTxt)
			}

			finalOriginalTitle := originalTitleEng
			if finalOriginalTitle == "" {
				finalOriginalTitle = utils.CleanTitle(title)
			}

			displayTitle := fmt.Sprintf("%s %s %s %s", finalOriginalTitle, year, quality, strings.Join(schema.AudioToString(magnetAudio), " "))
			if season != "" {
				displayTitle = fmt.Sprintf("%s S%02s", finalOriginalTitle, season)
			}
			displayTitle = utils.CleanTitle(displayTitle)

			var mySize string
			if len(size) == len(magnetLinks) {
				mySize = size[it]
			}
			if mySize == "" {
				go func() { _, _ = i.magnetMetadataAPI.FetchMetadata(ctx, magnetLink) }()
			}

			category := 2000 // Movies
			if season != "" {
				category = 5000 // TV
			}

			ixt := schema.IndexedTorrent{
				Title:         displayTitle,
				OriginalTitle: finalOriginalTitle,
				Details:       link,
				Year:          year,
				IMDB:          imdbLink,
				Category:      category,
				Audio:         magnetAudio,
				MagnetLink:    magnetLink,
				Date:          date,
				InfoHash:      infoHash,
				Trackers:      trackers,
				LeechCount:    peer,
				SeedCount:     seed,
				Peers:         peer,
				Seeders:       seed,
				Size:          mySize,
				SizeInBytes:   utils.ParseSize(mySize),
			}
			chanIndexedTorrent <- ixt
		}(it, magnetLink)
	}

	go func() {
		wg.Wait()
		close(chanIndexedTorrent)
	}()

	for it := range chanIndexedTorrent {
		indexedTorrents = append(indexedTorrents, it)
	}

	return indexedTorrents, nil
}
