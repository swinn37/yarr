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
	
	// Implémentation d'une logique de retry pour les erreurs DNS
	var resp *http.Response
	var lastErr error
	maxRetries := 3
	retryDelay := 2 * time.Second
	
	for i := 0; i < maxRetries; i++ {
		resp, lastErr = c.httpClient.Do(req)
		if lastErr == nil {
			return resp, nil
		}
		
		// Vérifier si l'erreur est liée à DNS ou à la connexion réseau
		if netErr, ok := lastErr.(net.Error); ok && (netErr.Timeout() || netErr.Temporary()) {
			// Attendre avant de réessayer
			time.Sleep(retryDelay)
			// Augmenter le délai pour la prochaine tentative (backoff exponentiel)
			retryDelay *= 2
			continue
		}
		
		// Vérifier spécifiquement les erreurs DNS comme "server misbehaving"
		errStr := lastErr.Error()
		if strings.Contains(errStr, "dial tcp") && 
		   (strings.Contains(errStr, "lookup") || 
		    strings.Contains(errStr, "server misbehaving") || 
		    strings.Contains(errStr, "no such host") || 
		    strings.Contains(errStr, "i/o timeout")) {
			// C'est probablement une erreur DNS, réessayer
			time.Sleep(retryDelay)
			retryDelay *= 2
			continue
		}
		
		// Si ce n'est pas une erreur réseau temporaire, ne pas réessayer
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
			Timeout:   30 * time.Second, // Augmentation du timeout DNS de 10 à 30 secondes
			KeepAlive: 30 * time.Second,
			DualStack: true,             // Activer IPv4 et IPv6
		}).DialContext,
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   time.Second * 20,
		ResponseHeaderTimeout: time.Second * 20,
		ExpectContinueTimeout: time.Second * 10,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
	}
	httpClient := &http.Client{
		Timeout:   time.Second * 60, // Augmentation du timeout global de 30 à 60 secondes
		Transport: transport,
	}
	client = &Client{
		httpClient: httpClient,
		userAgent:  "Yarr/1.0",
	}
}
