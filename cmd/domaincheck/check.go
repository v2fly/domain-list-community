package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const bootstrapURL = "https://data.iana.org/rdap/dns.json"

type checkResult struct {
	*domainEntry
	Status       string    `json:"status"`
	RDAPURL      string    `json:"rdap_url,omitempty"`
	HTTPStatus   int       `json:"http_status,omitempty"`
	Registered   time.Time `json:"registered"`
	DNS          []string  `json:"dns,omitempty"`
	RegistryHold []string  `json:"registry_status,omitempty"`
	Detail       string    `json:"detail,omitempty"`
}

type rdapResponse struct {
	ObjectClassName string   `json:"objectClassName"`
	LDHName         string   `json:"ldhName"`
	Status          []string `json:"status"`
	ErrorCode       int      `json:"errorCode"`
	Events          []struct {
		Action string `json:"eventAction"`
		Date   string `json:"eventDate"`
	} `json:"events"`
}

type hostLimiter struct {
	mu   sync.Mutex
	next time.Time
}

func (limiter *hostLimiter) wait(ctx context.Context, interval time.Duration) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		limiter.mu.Lock()
		delay := time.Until(limiter.next)
		if delay <= 0 {
			limiter.next = time.Now().Add(interval)
			limiter.mu.Unlock()
			return nil
		}
		limiter.mu.Unlock()
		if err := pause(ctx, delay); err != nil {
			return err
		}
	}
}

func (limiter *hostLimiter) deferUntil(until time.Time) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if until.After(limiter.next) {
		limiter.next = until
	}
}

type checker struct {
	client    *http.Client
	services  map[string]string
	interval  time.Duration
	limiters  sync.Map
	resolvers []string
}

func (checker *checker) limiterFor(host string) *hostLimiter {
	value, _ := checker.limiters.LoadOrStore(host, &hostLimiter{})
	return value.(*hostLimiter)
}

func newChecker(ctx context.Context, client *http.Client, interval time.Duration) (*checker, error) {
	result := &checker{
		client: client, interval: interval, services: make(map[string]string),
		resolvers: []string{"https://dns.google/resolve", "https://cloudflare-dns.com/dns-query"},
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(request *http.Request, previous []*http.Request) error {
		if len(previous) >= 10 || request.URL.Scheme != "https" {
			return fmt.Errorf("refusing excessive redirects or an insecure redirect")
		}
		if client.CheckRedirect != nil {
			if err := client.CheckRedirect(request, previous); err != nil {
				return err
			}
		}
		return result.limiterFor(request.URL.Host).wait(request.Context(), interval)
	}
	result.client = &clientCopy
	var bootstrap struct {
		Services [][][]string `json:"services"`
	}
	status, err := result.getJSON(ctx, bootstrapURL, &bootstrap)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("bootstrap returned HTTP %d", status)
	}
	for _, service := range bootstrap.Services {
		if len(service) != 2 {
			return nil, fmt.Errorf("invalid RDAP bootstrap service")
		}
		for _, endpoint := range service[1] {
			parsed, err := url.Parse(endpoint)
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
				continue
			}
			for _, suffix := range service[0] {
				result.services[strings.ToLower(suffix)] = strings.TrimRight(endpoint, "/")
			}
			break
		}
	}
	if len(result.services) == 0 {
		return nil, fmt.Errorf("RDAP bootstrap contains no HTTPS services")
	}
	return result, nil
}

func pause(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryDelay(value string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil && time.Until(date) > 0 {
		return time.Until(date)
	}
	return time.Duration(1<<attempt) * time.Second
}

func (checker *checker) getJSON(ctx context.Context, endpoint string, target any) (int, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return 0, err
	}
	limiter := checker.limiterFor(parsed.Host)
	for attempt := 0; attempt < 3; attempt++ {
		if err := limiter.wait(ctx, checker.interval); err != nil {
			return 0, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return 0, err
		}
		request.Header.Set("Accept", "application/rdap+json, application/json")
		if parsed.Query().Get("type") == "NS" {
			request.Header.Set("Accept", "application/dns-json")
		}
		request.Header.Set("User-Agent", "domain-list-community-domain-audit/1.0 (+https://github.com/v2fly/domain-list-community)")
		response, requestErr := checker.client.Do(request)
		if requestErr != nil {
			if attempt < 2 && ctx.Err() == nil {
				limiter.deferUntil(time.Now().Add(retryDelay("", attempt)))
				continue
			}
			return 0, requestErr
		}
		status := response.StatusCode
		if status == http.StatusTooManyRequests || status >= 500 {
			_ = response.Body.Close()
			until := time.Now().Add(retryDelay(response.Header.Get("Retry-After"), attempt))
			limiter.deferUntil(until)
			checker.limiterFor(response.Request.URL.Host).deferUntil(until)
			if attempt < 2 {
				continue
			}
			return status, fmt.Errorf("HTTP %d after three attempts", status)
		}
		// A bare proxy/web-server 404 is not evidence of a missing RDAP object.
		if status != http.StatusOK && status != http.StatusNotFound {
			_ = response.Body.Close()
			return status, fmt.Errorf("HTTP %d", status)
		}
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 2*1024*1024)).Decode(target)
		_ = response.Body.Close()
		return status, decodeErr
	}
	return 0, fmt.Errorf("request attempts exhausted")
}

func (checker *checker) rdapURL(domain string) string {
	for suffix := domain; suffix != ""; {
		if endpoint := checker.services[suffix]; endpoint != "" {
			return endpoint + "/domain/" + url.PathEscape(domain)
		}
		_, rest, found := strings.Cut(suffix, ".")
		if !found {
			break
		}
		suffix = rest
	}
	return ""
}

func (checker *checker) dnsStatus(ctx context.Context, resolver, domain string) string {
	endpoint := resolver + "?name=" + url.QueryEscape(domain+".") + "&type=NS"
	var response struct {
		Status   *int `json:"Status"`
		Question []struct {
			Name string `json:"name"`
			Type int    `json:"type"`
		} `json:"Question"`
	}
	status, err := checker.getJSON(ctx, endpoint, &response)
	if err != nil || status != http.StatusOK || response.Status == nil {
		return "unknown"
	}
	if len(response.Question) != 1 || !strings.EqualFold(strings.TrimSuffix(response.Question[0].Name, "."), domain) || response.Question[0].Type != 2 {
		return "unknown"
	}
	switch *response.Status {
	case 0:
		return "NOERROR"
	case 3:
		return "NXDOMAIN"
	default:
		return fmt.Sprintf("RCODE_%d", *response.Status)
	}
}

func (checker *checker) check(ctx context.Context, entry *domainEntry) checkResult {
	result := checkResult{domainEntry: entry, Status: "unknown"}
	result.RDAPURL = checker.rdapURL(entry.Domain)
	if result.RDAPURL == "" {
		result.Status = "unsupported"
		result.Detail = "No HTTPS RDAP service in IANA bootstrap"
		return result
	}
	var response rdapResponse
	status, err := checker.getJSON(ctx, result.RDAPURL, &response)
	result.HTTPStatus = status
	if err != nil {
		result.Detail = err.Error()
		return result
	}
	if status == http.StatusNotFound {
		if response.ErrorCode != http.StatusNotFound {
			result.Detail = "HTTP 404 without an RDAP errorCode of 404"
			return result
		}
		for _, resolver := range checker.resolvers {
			result.DNS = append(result.DNS, checker.dnsStatus(ctx, resolver, entry.Domain))
		}
		if len(result.DNS) == 2 && result.DNS[0] == "NXDOMAIN" && result.DNS[1] == "NXDOMAIN" {
			result.Status = "possibly_unregistered"
			result.Detail = "Registry RDAP 404 and NXDOMAIN from two resolvers; review before removal"
		} else {
			result.Detail = "Registry RDAP 404 without two NXDOMAIN confirmations"
		}
		return result
	}
	if response.ObjectClassName != "domain" || !strings.EqualFold(strings.TrimSuffix(response.LDHName, "."), entry.Domain) {
		result.Detail = "RDAP response does not identify the queried domain"
		return result
	}
	result.Status = "registered"
	result.RegistryHold = response.Status
	for _, event := range response.Events {
		if event.Action != "registration" {
			continue
		}
		registered, err := time.Parse(time.RFC3339, event.Date)
		if err != nil {
			result.Status = "unknown"
			result.Detail = "Invalid RDAP registration event date"
			return result
		}
		if result.Registered.IsZero() || registered.Before(result.Registered) {
			result.Registered = registered
		}
	}
	// Registry timestamps and commit clocks may differ; require a full day of separation.
	if !entry.FirstSeen.IsZero() && result.Registered.After(entry.FirstSeen.Add(24*time.Hour)) {
		result.Status = "possibly_reregistered"
		result.Detail = "Registration postdates earliest rule history by more than 24 hours; ownership needs manual verification"
	}
	return result
}
