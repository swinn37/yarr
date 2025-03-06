package worker

import (
	"net"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	httpClient *http.Client
	userAgent  string
}

func (c *Client) get(url string) (*http.Response, error) {
	return c.getConditional(url, "", "")
}

func (c *Client) getConditional(url, lastModified, etag string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	
	// Implementation of a retry logic for DNS errors
	var resp *http.Response
	var lastErr error
	maxRetries := 3
	retryDelay := 2 * time.Second
	
	for i := 0; i < maxRetries; i++ {
		resp, lastErr = c.httpClient.Do(req)
		if lastErr == nil {
			return resp, nil
		}
		
		// Check if the error is related to DNS or network connection
		if netErr, ok := lastErr.(net.Error); ok && (netErr.Timeout() || netErr.Temporary()) {
			// Wait before retrying
			time.Sleep(retryDelay)
			// Increase delay for the next attempt (exponential backoff)
			retryDelay *= 2
			continue
		}
		
		// Specifically check for DNS errors like "server misbehaving"
		errStr := lastErr.Error()
		if strings.Contains(errStr, "dial tcp") && 
		   (strings.Contains(errStr, "lookup") || 
		    strings.Contains(errStr, "server misbehaving") || 
		    strings.Contains(errStr, "no such host") || 
		    strings.Contains(errStr, "i/o timeout")) {
			// This is probably a DNS error, retry
			time.Sleep(retryDelay)
			retryDelay *= 2
			continue
		}
		
		// If it's not a temporary network error, don't retry
		return nil, lastErr
	}
	
	return nil, lastErr
}

var client *Client

func SetVersion(num string) {
	client.userAgent = "Yarr/" + num
}

func init() {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			DualStack: true,             // Enable both IPv4 and IPv6
		}).DialContext,
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   time.Second * 20,
		ResponseHeaderTimeout: time.Second * 20,
		ExpectContinueTimeout: time.Second * 10,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
	}
	httpClient := &http.Client{
		Timeout:   time.Second * 60,
		Transport: transport,
	}
	client = &Client{
		httpClient: httpClient,
		userAgent:  "Yarr/1.0",
	}
}
