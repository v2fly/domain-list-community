package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const (
	acceptJSON   = "application/json"
	acceptRDAP   = "application/rdap+json, application/json"
	acceptDNS    = "application/dns-json"
	userAgent    = "domain-list-community-domain-audit/1.0 (+https://github.com/v2fly/domain-list-community)"
	maxJSONBytes = 2 * 1024 * 1024
	maxAttempts  = 3
	maxRedirects = 10
)

var (
	errRedirectPolicy = errors.New("redirect refused")
	errEmptyBody      = errors.New("empty response body")
)

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

func (checker *checker) limiterFor(host string) *hostLimiter {
	value, _ := checker.limiters.LoadOrStore(host, &hostLimiter{})
	return value.(*hostLimiter)
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

// newRequest builds every hop from scratch so cookies or sensitive headers cannot leak across hosts.
func newRequest(ctx context.Context, endpoint, accept string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("User-Agent", userAgent)
	return request, nil
}

func (checker *checker) get(ctx context.Context, endpoint, accept string) (*http.Response, error) {
	request, err := newRequest(ctx, endpoint, accept)
	if err != nil {
		return nil, err
	}
	client := *checker.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for hop := 1; ; hop++ {
		// Start each network timeout only after this hop's pacing delay has elapsed.
		if err := checker.limiterFor(request.URL.Host).wait(ctx, checker.interval); err != nil {
			return nil, err
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		if response.Request == nil {
			response.Request = request
		}
		switch response.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			if response.Header.Get("Location") == "" {
				return response, nil
			}
		default:
			return response, nil
		}
		location, err := response.Location()
		_ = response.Body.Close()
		switch {
		case err != nil:
			return nil, fmt.Errorf("%w: %v", errRedirectPolicy, err)
		case location.Scheme != "https":
			return nil, fmt.Errorf("%w: insecure redirect to %s", errRedirectPolicy, location.Redacted())
		case hop >= maxRedirects:
			return nil, fmt.Errorf("%w: stopped after %d redirects", errRedirectPolicy, maxRedirects)
		}
		if request, err = newRequest(ctx, location.String(), accept); err != nil {
			return nil, err
		}
	}
}

// getJSON decodes a complete, bounded 200 or 404 response. An empty body returns errEmptyBody.
func (checker *checker) getJSON(ctx context.Context, endpoint, accept string, target any) (int, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return 0, err
	}
	limiter := checker.limiterFor(parsed.Host)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		response, requestErr := checker.get(ctx, endpoint, accept)
		if requestErr != nil {
			if attempt < maxAttempts-1 && ctx.Err() == nil && !errors.Is(requestErr, errRedirectPolicy) {
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
			if attempt < maxAttempts-1 {
				continue
			}
			return status, fmt.Errorf("HTTP %d after %d attempts", status, maxAttempts)
		}
		if status != http.StatusOK && status != http.StatusNotFound {
			_ = response.Body.Close()
			return status, fmt.Errorf("HTTP %d", status)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxJSONBytes+1))
		closeErr := response.Body.Close()
		if readErr != nil {
			return status, fmt.Errorf("read JSON response: %w", readErr)
		}
		if closeErr != nil {
			return status, fmt.Errorf("close JSON response: %w", closeErr)
		}
		if len(body) > maxJSONBytes {
			return status, fmt.Errorf("JSON response exceeds %d bytes", maxJSONBytes)
		}
		if len(bytes.TrimSpace(body)) == 0 {
			return status, errEmptyBody
		}
		return status, json.Unmarshal(body, target)
	}
	return 0, fmt.Errorf("request attempts exhausted")
}
