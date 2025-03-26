package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

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

	// Check that the URL is valid
	_, err := url.Parse(candidateUrl)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %v", err)
	}

	// Query URL
	res, err := client.get(candidateUrl)
	if err != nil {
		// Improve error message for DNS problems
		if strings.Contains(err.Error(), "dial tcp") && strings.Contains(err.Error(), "lookup") {
			return nil, fmt.Errorf("DNS resolution problem for %s: %v (check your network connection or DNS servers)", candidateUrl, err)
		}
		return nil, fmt.Errorf("error retrieving feed: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != 200 {
		return nil, fmt.Errorf("server responded with code %d", res.StatusCode)
	}
	cs := getCharset(res)

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading content: %v", err)
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
		return nil, errors.New("no feeds found at the given url")
	case len(sources) == 1:
		if sources[0].Url == candidateUrl {
			return nil, errors.New("recursion")
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

// findFaviconWithContext is a context-aware version of findFavicon
func findFaviconWithContext(ctx context.Context, siteUrl, feedUrl string) (*[]byte, error) {
	urls := make([]string, 0)
	var lastErr error

	favicon := func(link string) string {
		u, err := url.Parse(link)
		if err != nil {
			lastErr = fmt.Errorf("invalid URL for favicon: %v", err)
			return ""
		}
		return fmt.Sprintf("%s://%s/favicon.ico", u.Scheme, u.Host)
	}

	if siteUrl != "" {
		res, err := client.getWithContext(ctx, siteUrl, "", "")
		if err != nil {
			// Check if the error is due to context cancellation
			if ctx.Err() != nil {
				return &emptyIcon, ctx.Err()
			}
			// Log the error but continue with other potential sources
			lastErr = fmt.Errorf("error accessing site %s: %v", siteUrl, err)
		} else {
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			if err != nil {
				lastErr = fmt.Errorf("error reading site content %s: %v", siteUrl, err)
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

	// If no icon URL was found, return the last error
	if len(urls) == 0 && lastErr != nil {
		return &emptyIcon, fmt.Errorf("unable to find icons: %v", lastErr)
	}

	for _, u := range urls {
		// Check for context cancellation before making request
		if ctx.Err() != nil {
			return &emptyIcon, ctx.Err()
		}

		res, err := client.getWithContext(ctx, u, "", "")
		if err != nil {
			// Check if the error is due to context cancellation
			if ctx.Err() != nil {
				return &emptyIcon, ctx.Err()
			}
			lastErr = fmt.Errorf("error accessing icon %s: %v", u, err)
			continue
		}
		defer res.Body.Close()

		if res.StatusCode != 200 {
			lastErr = fmt.Errorf("status code %d for icon %s", res.StatusCode, u)
			continue
		}

		content, err := io.ReadAll(res.Body)
		if err != nil {
			lastErr = fmt.Errorf("error reading icon %s: %v", u, err)
			continue
		}

		ctype := http.DetectContentType(content)
		if imageTypes[ctype] {
			return &content, nil
		} else {
			lastErr = fmt.Errorf("unsupported content type for icon %s: %s", u, ctype)
		}
	}

	if lastErr != nil {
		return &emptyIcon, fmt.Errorf("no valid icon found: %v", lastErr)
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

// listItemsWithContext is a context-aware version of listItems that accepts a context for cancellation
func listItemsWithContext(ctx context.Context, f storage.Feed, db *storage.Storage) ([]storage.Item, error) {
	// Track start time for performance monitoring
	startTime := time.Now()
	var fetchTime, parseTime time.Duration

	lmod := ""
	etag := ""
	if state := db.GetHTTPState(f.Id); state != nil {
		lmod = state.LastModified
		etag = state.Etag
	}

	// Fetch the feed content with the provided context
	fetchStartTime := time.Now()
	res, err := client.getWithContext(ctx, f.FeedLink, lmod, etag)
	if err != nil {
		// Check if the error is due to context cancellation
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// Improve error message for DNS problems
		if strings.Contains(err.Error(), "dial tcp") && strings.Contains(err.Error(), "lookup") {
			return nil, fmt.Errorf("DNS resolution problem for %s: %v (check your network connection or DNS servers)", f.FeedLink, err)
		}
		// Improve error message for other network errors
		if strings.Contains(err.Error(), "i/o timeout") {
			return nil, fmt.Errorf("timeout connecting to %s: %v (server may be overloaded or unreachable)", f.FeedLink, err)
		}
		return nil, fmt.Errorf("error retrieving feed %s: %v", f.FeedLink, err)
	}
	defer res.Body.Close()
	fetchTime = time.Since(fetchStartTime)

	switch {
	case res.StatusCode < 200 || res.StatusCode > 399:
		if res.StatusCode == 404 {
			return nil, fmt.Errorf("feed not found (404) for %s", f.FeedLink)
		}
		return nil, fmt.Errorf("server responded with code %d for %s", res.StatusCode, f.FeedLink)
	case res.StatusCode == http.StatusNotModified:
		return nil, nil
	}

	// Check for context cancellation before parsing
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Parse the feed content
	parseStartTime := time.Now()
	feed, err := parser.ParseAndFix(res.Body, f.FeedLink, getCharset(res))
	if err != nil {
		return nil, err
	}
	parseTime = time.Since(parseStartTime)

	lmod = res.Header.Get("Last-Modified")
	etag = res.Header.Get("Etag")
	if lmod != "" || etag != "" {
		db.SetHTTPState(f.Id, lmod, etag)
	}

	// Convert items and log detailed timing information for slow feeds
	items := ConvertItems(feed.Items, f)
	totalTime := time.Since(startTime)

	// Log only basic information for slow feeds
	if totalTime > 10*time.Second {
		log.Printf("SLOW FEED: %s (ID: %d) - Total: %v (Fetch: %v, Parse: %v) - URL: %s",
			f.Title, f.Id, totalTime, fetchTime, parseTime, f.FeedLink)
	}

	return items, nil
}

// The original listItems function now calls the context-aware version with a background context
func listItems(f storage.Feed, db *storage.Storage) ([]storage.Item, error) {
	return listItemsWithContext(context.Background(), f, db)
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

// GetBodyWithContext is a context-aware version of GetBody
func GetBodyWithContext(ctx context.Context, urlStr string) (string, error) {
	// Check that the URL is valid
	_, err := url.Parse(urlStr)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %v", err)
	}

	res, err := client.getWithContext(ctx, urlStr, "", "")
	if err != nil {
		// Check if the error is due to context cancellation
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		// Improve error message for DNS problems
		if strings.Contains(err.Error(), "dial tcp") && strings.Contains(err.Error(), "lookup") {
			return "", fmt.Errorf("DNS resolution problem for %s: %v (check your network connection or DNS servers)", urlStr, err)
		}
		// Improve error message for other network errors
		if strings.Contains(err.Error(), "i/o timeout") {
			return "", fmt.Errorf("timeout connecting to %s: %v (server may be overloaded or unreachable)", urlStr, err)
		}
		return "", fmt.Errorf("error retrieving page %s: %v", urlStr, err)
	}
	defer res.Body.Close()

	if res.StatusCode != 200 {
		return "", fmt.Errorf("server responded with code %d for %s", res.StatusCode, urlStr)
	}

	var r io.Reader

	ctype := res.Header.Get("Content-Type")
	if strings.Contains(ctype, "charset") {
		r, err = charset.NewReader(res.Body, ctype)
		if err != nil {
			return "", fmt.Errorf("error decoding charset: %v", err)
		}
	} else {
		r = res.Body
	}

	// Check for context cancellation before reading body
	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	body, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("error reading content: %v", err)
	}
	return string(body), nil
}

// The original GetBody function now calls the context-aware version with a background context
func GetBody(urlStr string) (string, error) {
	return GetBodyWithContext(context.Background(), urlStr)
}
