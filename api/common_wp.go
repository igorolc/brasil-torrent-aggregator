package handler

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/PuerkitoBio/goquery"
	"github.com/felipemarinho97/torrent-indexer/logging"
	"github.com/felipemarinho97/torrent-indexer/magnet"
	"github.com/felipemarinho97/torrent-indexer/schema"
	goscrape "github.com/felipemarinho97/torrent-indexer/scrape"
	"github.com/felipemarinho97/torrent-indexer/utils"
)

// Common logic for RedeTorrent and SemTorrent as they share the same layout
func (i *Indexer) fetchGenericWPIndexer(ctx context.Context, q, page string, metadata IndexerMeta) ([]schema.IndexedTorrent, error) {
	searchQ := url.QueryEscape(CleanSearchQuery(q))
	targetUrl := metadata.URL
	if searchQ != "" {
		if !strings.HasSuffix(targetUrl, "/") {
			targetUrl += "/"
		}
		targetUrl = fmt.Sprintf("%s%s%s", targetUrl, metadata.SearchURL, searchQ)
	} else if page != "" {
		targetUrl = fmt.Sprintf(fmt.Sprintf("%s%s", targetUrl, metadata.PagePattern), page)
	}

	resp, err := i.requester.GetDocument(ctx, targetUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Close()

	doc, err := goquery.NewDocumentFromReader(resp)
	if err != nil {
		return nil, err
	}

	listSelector := metadata.ListSelector
	if listSelector == "" {
		listSelector = ".capa_lista"
	}

	var links []string
	doc.Find(listSelector).Each(func(idx int, s *goquery.Selection) {
		link, _ := s.Find("a").Attr("href")
		if link != "" {
			links = append(links, link)
		}
	})

	if len(links) == 0 {
		_ = i.requester.ExpireDocument(ctx, targetUrl)
	}

	indexedTorrents := utils.ParallelFlatMap(links, func(link string) ([]schema.IndexedTorrent, error) {
		return getTorrentsGenericWP(ctx, i, link, targetUrl, metadata.Label)
	})

	return indexedTorrents, nil
}

func getTorrentsGenericWP(ctx context.Context, i *Indexer, link, referer, label string) ([]schema.IndexedTorrent, error) {
	var indexedTorrents []schema.IndexedTorrent
	doc, err := getDocument(ctx, i, link, referer)
	if err != nil {
		return nil, err
	}

	containerSelector := metadata.ContainerSelector
	if containerSelector == "" {
		containerSelector = ".conteudo"
	}
	article := doc.Find(containerSelector)
	if article.Length() == 0 {
		// Fallback for HDR Torrent if not specified
		article = doc.Find("main")
	}

	cleanStr := func(s string) string {
		s = regexp.MustCompile(`[\n\t\r]+`).ReplaceAllString(s, " ")
		s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
		return strings.TrimSpace(s)
	}

	h1Text := cleanStr(article.Find("h1").First().Text())
	titleRe := regexp.MustCompile(`^(.*?)(?:\s*-\s*(.*?))?\s*\((\d{4})\)`)
	titleP := titleRe.FindStringSubmatch(h1Text)
	
	var title, year string
	if len(titleP) >= 4 {
		title = cleanStr(titleP[1])
		year = cleanStr(titleP[3])
	} else {
		title = h1Text
		year = findYearFromText(h1Text, "")
	}

	date := getPublishedDateFromMeta(doc)
	pageTitle := doc.Find("title").Text()

	originalTitleEng := ""
	fullTxt := article.Text()
	match := regexp.MustCompile(`(?i)T.tulo\s*Original\s*[:\s-]*([^·\n\t\r<|/]+)`).FindStringSubmatch(fullTxt)
	if len(match) > 1 {
		originalTitleEng = utils.CleanOriginalTitle(match[1])
	}

	type magnetWithContext struct {
		link    string
		context string
	}
	var magnetLinks []magnetWithContext
	
	article.Find("a.newdawn[href^=\"magnet\"]").Each(func(i int, s *goquery.Selection) {
		magnetLink, _ := s.Attr("href")
		contextTxt, _ := s.Attr("title")
		if strings.TrimSpace(contextTxt) == "" {
			contextTxt = s.Text()
		}
		magnetLinks = append(magnetLinks, magnetWithContext{
			link:    magnetLink,
			context: cleanStr(contextTxt),
		})
	})
	
	if len(magnetLinks) == 0 {
		article.Find("a[href^=\"magnet\"]").Each(func(i int, s *goquery.Selection) {
			magnetLink, _ := s.Attr("href")
			ctxVal, _ := s.Attr("title")
			if ctxVal == "" {
				ctxVal = s.Text()
			}
			magnetLinks = append(magnetLinks, magnetWithContext{
				link:    magnetLink,
				context: cleanStr(ctxVal),
			})
		})
	}

	var audio []schema.Audio
	var size []string
	var globalQuality string
	article.Find("div#informacoes > p").Each(func(i int, s *goquery.Selection) {
		text := s.Text()
		audio = append(audio, findAudioFromText(text)...)
		y := findYearFromText(text, title)
		if y != "" {
			year = strings.TrimSpace(y)
		}
		if globalQuality == "" {
			globalQuality = findQualityFromText(text)
		}
	size = append(size, findSizesFromText(text)...)
	})

	// Metadata fallback for HDR Torrent
	if globalQuality == "" {
		globalQuality = cleanStr(doc.Find(".box_qual").First().Text())
	}
	midia := doc.Find(".box_midia").Text()
	audio = append(audio, findAudioFromText(midia)...)

	imdbLink := ""
	article.Find("a").Each(func(i int, s *goquery.Selection) {
		link, _ := s.Attr("href")
		_imdbLink, err := getIMDBLink(link)
		if err == nil {
			imdbLink = _imdbLink
		}
	})

	size = utils.StableUniq(size)

	var chanIndexedTorrent = make(chan schema.IndexedTorrent, len(magnetLinks))
	var wg sync.WaitGroup

	for it, mCtx := range magnetLinks {
		wg.Add(1)
		go func(it int, magnetLink, contextText string) {
			defer wg.Done()
			magnet, err := magnet.ParseMagnetUri(magnetLink)
			if err != nil {
				return
			}
			releaseTitle := magnet.DisplayName
			infoHash := magnet.InfoHash.String()
			trackers := magnet.Trackers
			magnetAudio := getAudioFromTitle(releaseTitle, audio)

			baseTitle := title
			if originalTitleEng != "" && !strings.EqualFold(title, originalTitleEng) {
				baseTitle = fmt.Sprintf("%s.%s", title, originalTitleEng)
			}

			contextUpper := strings.ToUpper(contextText)
			qualityRegex := regexp.MustCompile(`(?i)(720P|1080P|2160P|4K|ULTRA HD|HDR|DV|WEB-DL|BLURAY|CAM|TS|HD|R5|5\.1)`)
			qMatches := qualityRegex.FindAllString(contextUpper, -1)
			
			audioRegex := regexp.MustCompile(`(?i)(DUAL|AUDIO|DUBLADO|LEGENDADO|PORTUGUES|INGLES|ENG|PT-BR)`)
			aMatches := audioRegex.FindAllString(contextUpper, -1)
			
			// Se o contexto do link for pobre, buscamos na página/título global
			if len(qMatches) == 0 {
				upperFull := strings.ToUpper(globalQuality + " " + pageTitle)
				if strings.Contains(upperFull, "WEB-DL") || strings.Contains(upperFull, "WEBDL") {
					qMatches = append(qMatches, "WEB-DL")
				}
				if strings.Contains(upperFull, "BLURAY") {
					qMatches = append(qMatches, "BluRay")
				}
				
				// Pegar a primeira resolução encontrada na página como fallback
				resMatch := regexp.MustCompile(`(?i)(720P|1080P|2160P|4K)`).FindString(upperFull)
				if resMatch != "" {
					qMatches = append(qMatches, resMatch)
				}
			}

			hasResolution := false
			resRegex := regexp.MustCompile(`(?i)(720P|1080P|2160P|4K)`)
			for _, q := range qMatches {
				if resRegex.MatchString(q) {
					hasResolution = true
					break
				}
			}
			if !hasResolution && (strings.Contains(upperFull, "WEB-DL") || strings.Contains(upperFull, "BLURAY")) {
				qMatches = append(qMatches, "1080p")
			}

			contextClean := strings.Join(utils.StableUniq(append(qMatches, aMatches...)), ".")
			contextClean = strings.ReplaceAll(contextClean, " ", ".")
			rebuildTitle := fmt.Sprintf("%s.%s.%s", strings.ReplaceAll(baseTitle, " ", "."), year, contextClean)
			rebuildTitle = regexp.MustCompile(`\.+`).ReplaceAllString(rebuildTitle, ".")
			rebuildTitle = strings.Trim(rebuildTitle, ".")
			
			if len(releaseTitle) < 20 || strings.Contains(strings.ToUpper(releaseTitle), "HIDRATORRENTS") {
				releaseTitle = rebuildTitle
			}
			
			releaseTitle = utils.CleanTitle(releaseTitle)

			peer, seed, err := goscrape.GetLeechsAndSeeds(ctx, i.redis, i.metrics, infoHash, trackers)
			if err != nil {
				logging.Error().Err(err).Str("info_hash", infoHash).Msg("Failed to get leechers and seeders")
			}

			var mySize string
			if len(size) == len(magnetLinks) {
				mySize = size[it]
			} else if len(size) == 1 {
				mySize = size[0]
			}
			
			if mySize == "" {
				go func() {
					_, _ = i.magnetMetadataAPI.FetchMetadata(ctx, magnetLink)
				}()
			}

			ixt := schema.IndexedTorrent{
				Title:         releaseTitle,
				OriginalTitle: baseTitle,
				Details:       link,
				Year:          year,
				IMDB:          imdbLink,
				Category:      2000,
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
				Indexer:       label,
			}
			chanIndexedTorrent <- ixt
		}(it, mCtx.link, mCtx.context)
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
