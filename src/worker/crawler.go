package worker

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/nkanaev/yarr/src/content/scraper"
	"github.com/nkanaev/yarr/src/parser"
	"github.com/nkanaev/yarr/src/storage"
	"golang.org/x/net/html/charset"
)

type FeedSource struct {
	Title string `json:"title"`
	Url   string `json:"url"`
}

type DiscoverResult struct {
	Feed     *parser.Feed
	FeedLink string
	Sources  []FeedSource
}

func DiscoverFeed(candidateUrl string) (*DiscoverResult, error) {
	result := &DiscoverResult{}
	
	// Vérifier que l'URL est valide
	_, err := url.Parse(candidateUrl)
	if err != nil {
		return nil, fmt.Errorf("URL invalide: %v", err)
	}
	
	// Query URL
	res, err := client.get(candidateUrl)
	if err != nil {
		// Améliorer le message d'erreur pour les problèmes DNS
		if strings.Contains(err.Error(), "dial tcp") && strings.Contains(err.Error(), "lookup") {
			return nil, fmt.Errorf("problème de résolution DNS pour %s: %v (vérifiez votre connexion réseau ou les serveurs DNS)", candidateUrl, err)
		}
		return nil, fmt.Errorf("erreur lors de la récupération du flux: %v", err)
	}
	defer res.Body.Close()
	
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("le serveur a répondu avec le code %d", res.StatusCode)
	}
	cs := getCharset(res)

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("erreur lors de la lecture du contenu: %v", err)
	}

	// Try to feed into parser
	feed, err := parser.ParseAndFix(bytes.NewReader(body), candidateUrl, cs)
	if err == nil {
		result.Feed = feed
		result.FeedLink = candidateUrl
		return result, nil
	}

	// Possibly an html link. Search for feed links
	content := string(body)
	if cs != "" {
		if r, err := charset.NewReaderLabel(cs, bytes.NewReader(body)); err == nil {
			if body, err := io.ReadAll(r); err == nil {
				content = string(body)
			}
		}
	}
	sources := make([]FeedSource, 0)
	for url, title := range scraper.FindFeeds(content, candidateUrl) {
		sources = append(sources, FeedSource{Title: title, Url: url})
	}
	switch {
	case len(sources) == 0:
		return nil, errors.New("No feeds found at the given url")
	case len(sources) == 1:
		if sources[0].Url == candidateUrl {
			return nil, errors.New("Recursion!")
		}
		return DiscoverFeed(sources[0].Url)
	}

	result.Sources = sources
	return result, nil
}

var emptyIcon = make([]byte, 0)
var imageTypes = map[string]bool{
	"image/x-icon": true,
	"image/png":    true,
	"image/jpeg":   true,
	"image/gif":    true,
}

func findFavicon(siteUrl, feedUrl string) (*[]byte, error) {
	urls := make([]string, 0)
	var lastErr error

	favicon := func(link string) string {
		u, err := url.Parse(link)
		if err != nil {
			lastErr = fmt.Errorf("URL invalide pour favicon: %v", err)
			return ""
		}
		return fmt.Sprintf("%s://%s/favicon.ico", u.Scheme, u.Host)
	}

	if siteUrl != "" {
		res, err := client.get(siteUrl)
		if err != nil {
			// Enregistrer l'erreur mais continuer avec d'autres sources potentielles
			lastErr = fmt.Errorf("erreur lors de l'accès au site %s: %v", siteUrl, err)
		} else {
			defer res.Body.Close()
			body, err := ioutil.ReadAll(res.Body)
			if err != nil {
				lastErr = fmt.Errorf("erreur lors de la lecture du contenu du site %s: %v", siteUrl, err)
			} else {
				urls = append(urls, scraper.FindIcons(string(body), siteUrl)...)
				if c := favicon(siteUrl); c != "" {
					urls = append(urls, c)
				}
			}
		}
	}

	if c := favicon(feedUrl); c != "" {
		urls = append(urls, c)
	}

	// Si aucune URL d'icône n'a été trouvée, retourner la dernière erreur
	if len(urls) == 0 && lastErr != nil {
		return &emptyIcon, fmt.Errorf("impossible de trouver des icônes: %v", lastErr)
	}

	for _, u := range urls {
		res, err := client.get(u)
		if err != nil {
			lastErr = fmt.Errorf("erreur lors de l'accès à l'icône %s: %v", u, err)
			continue
		}
		defer res.Body.Close()
		
		if res.StatusCode != 200 {
			lastErr = fmt.Errorf("code de statut %d pour l'icône %s", res.StatusCode, u)
			continue
		}

		content, err := ioutil.ReadAll(res.Body)
		if err != nil {
			lastErr = fmt.Errorf("erreur lors de la lecture de l'icône %s: %v", u, err)
			continue
		}

		ctype := http.DetectContentType(content)
		if imageTypes[ctype] {
			return &content, nil
		} else {
			lastErr = fmt.Errorf("type de contenu non supporté pour l'icône %s: %s", u, ctype)
		}
	}
	
	if lastErr != nil {
		return &emptyIcon, fmt.Errorf("aucune icône valide trouvée: %v", lastErr)
	}
	return &emptyIcon, nil
}

func ConvertItems(items []parser.Item, feed storage.Feed) []storage.Item {
	result := make([]storage.Item, len(items))
	for i, item := range items {
		item := item
		var audioURL *string = nil
		if item.AudioURL != "" {
			audioURL = &item.AudioURL
		}
		var imageURL *string = nil
		if item.ImageURL != "" {
			imageURL = &item.ImageURL
		}
		result[i] = storage.Item{
			GUID:     item.GUID,
			FeedId:   feed.Id,
			Title:    item.Title,
			Link:     item.URL,
			Content:  item.Content,
			Date:     item.Date,
			Status:   storage.UNREAD,
			ImageURL: imageURL,
			AudioURL: audioURL,
		}
	}
	return result
}

func listItems(f storage.Feed, db *storage.Storage) ([]storage.Item, error) {
	lmod := ""
	etag := ""
	if state := db.GetHTTPState(f.Id); state != nil {
		lmod = state.LastModified
		etag = state.Etag
	}

	res, err := client.getConditional(f.FeedLink, lmod, etag)
	if err != nil {
		// Améliorer le message d'erreur pour les problèmes DNS
		if strings.Contains(err.Error(), "dial tcp") && strings.Contains(err.Error(), "lookup") {
			return nil, fmt.Errorf("problème de résolution DNS pour %s: %v (vérifiez votre connexion réseau ou les serveurs DNS)", f.FeedLink, err)
		}
		// Améliorer le message pour les autres erreurs réseau
		if strings.Contains(err.Error(), "i/o timeout") {
			return nil, fmt.Errorf("timeout lors de la connexion à %s: %v (le serveur est peut-être surchargé ou inaccessible)", f.FeedLink, err)
		}
		return nil, fmt.Errorf("erreur lors de la récupération du flux %s: %v", f.FeedLink, err)
	}
	defer res.Body.Close()

	switch {
	case res.StatusCode < 200 || res.StatusCode > 399:
		if res.StatusCode == 404 {
			return nil, fmt.Errorf("flux introuvable (404) pour %s", f.FeedLink)
		}
		return nil, fmt.Errorf("le serveur a répondu avec le code %d pour %s", res.StatusCode, f.FeedLink)
	case res.StatusCode == http.StatusNotModified:
		return nil, nil
	}

	feed, err := parser.ParseAndFix(res.Body, f.FeedLink, getCharset(res))
	if err != nil {
		return nil, err
	}

	lmod = res.Header.Get("Last-Modified")
	etag = res.Header.Get("Etag")
	if lmod != "" || etag != "" {
		db.SetHTTPState(f.Id, lmod, etag)
	}
	return ConvertItems(feed.Items, f), nil
}

func getCharset(res *http.Response) string {
	contentType := res.Header.Get("Content-Type")
	if _, params, err := mime.ParseMediaType(contentType); err == nil {
		if cs, ok := params["charset"]; ok {
			if e, _ := charset.Lookup(cs); e != nil {
				return cs
			}
		}
	}
	return ""
}

func GetBody(urlStr string) (string, error) {
	// Vérifier que l'URL est valide
	_, err := url.Parse(urlStr)
	if err != nil {
		return "", fmt.Errorf("URL invalide: %v", err)
	}
	
	res, err := client.get(urlStr)
	if err != nil {
		// Améliorer le message d'erreur pour les problèmes DNS
		if strings.Contains(err.Error(), "dial tcp") && strings.Contains(err.Error(), "lookup") {
			return "", fmt.Errorf("problème de résolution DNS pour %s: %v (vérifiez votre connexion réseau ou les serveurs DNS)", urlStr, err)
		}
		// Améliorer le message pour les autres erreurs réseau
		if strings.Contains(err.Error(), "i/o timeout") {
			return "", fmt.Errorf("timeout lors de la connexion à %s: %v (le serveur est peut-être surchargé ou inaccessible)", urlStr, err)
		}
		return "", fmt.Errorf("erreur lors de la récupération de la page %s: %v", urlStr, err)
	}
	defer res.Body.Close()
	
	if res.StatusCode != 200 {
		return "", fmt.Errorf("le serveur a répondu avec le code %d pour %s", res.StatusCode, urlStr)
	}

	var r io.Reader

	ctype := res.Header.Get("Content-Type")
	if strings.Contains(ctype, "charset") {
		r, err = charset.NewReader(res.Body, ctype)
		if err != nil {
			return "", fmt.Errorf("erreur lors du décodage du charset: %v", err)
		}
	} else {
		r = res.Body
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("erreur lors de la lecture du contenu: %v", err)
	}
	return string(body), nil
}
