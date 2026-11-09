package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type auditHTTPTransport func(*http.Request) (*http.Response, error)

func (transport auditHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func auditHTTPResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestGetJSONRedirectPacingOutsideRequestTimeout(t *testing.T) {
	var sourceRequests, destinationRequests atomic.Int32
	client := &http.Client{
		Timeout: 50 * time.Millisecond,
		Transport: auditHTTPTransport(func(request *http.Request) (*http.Response, error) {
			switch request.URL.String() {
			case bootstrapURL:
				return auditHTTPResponse(200, `{"services":[[["com"],["https://source.example/"]]]}`), nil
			case "https://source.example/domain/example.com":
				sourceRequests.Add(1)
				// Simulate headers injected by a cookie jar or a transport.
				request.Header.Set("Cookie", "source-secret")
				request.Header.Set("Authorization", "source-secret")
				response := auditHTTPResponse(302, "")
				response.Header.Set("Location", "https://destination.example/domain/example.com")
				return response, nil
			case "https://destination.example/domain/example.com":
				destinationRequests.Add(1)
				if request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" {
					t.Error("sensitive source headers leaked to the redirect destination")
				}
				if request.Header.Get("Accept") != acceptRDAP || request.Header.Get("User-Agent") != userAgent {
					t.Errorf("redirect destination headers = %v, want RDAP Accept and audit User-Agent", request.Header)
				}
				if err := request.Context().Err(); err != nil {
					return nil, err
				}
				return auditHTTPResponse(404, `{"errorCode":404}`), nil
			default:
				return nil, fmt.Errorf("unexpected URL: %s", request.URL)
			}
		}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	checker, err := newChecker(ctx, client, time.Nanosecond)
	if err != nil {
		t.Fatalf("newChecker() = %v", err)
	}
	// This destination's cooldown exceeds an entire network request's timeout.
	checker.limiterFor("destination.example").deferUntil(time.Now().Add(150 * time.Millisecond))
	var result rdapResponse
	status, err := checker.getJSON(ctx, "https://source.example/domain/example.com", acceptRDAP, &result)
	if err != nil || status != 404 || result.ErrorCode != 404 {
		t.Fatalf("paced redirect = HTTP %d, %+v, %v", status, result, err)
	}
	if sourceRequests.Load() != 1 || destinationRequests.Load() != 1 {
		t.Errorf("redirect consumed retries: source=%d destination=%d", sourceRequests.Load(), destinationRequests.Load())
	}
}

func TestGetJSONFollowsTLSAndSameHostRedirects(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		switch request.URL.Path {
		case "/start":
			http.Redirect(writer, request, "/next", http.StatusTemporaryRedirect)
		case "/next":
			http.Redirect(writer, request, "/final", http.StatusPermanentRedirect)
		case "/final":
			fmt.Fprint(writer, `{"errorCode":404}`)
		default:
			t.Errorf("unexpected path: %s", request.URL.Path)
			writer.WriteHeader(400)
		}
	}))
	defer server.Close()
	checker := &checker{client: server.Client(), interval: time.Millisecond}
	var result rdapResponse
	status, err := checker.getJSON(context.Background(), server.URL+"/start", acceptRDAP, &result)
	if err != nil || status != 200 || result.ErrorCode != 404 || requests.Load() != 3 {
		t.Errorf("same-host TLS redirects = HTTP %d, %+v, %v, %d requests", status, result, err, requests.Load())
	}
}

func TestGetJSONEnforcesRedirectPolicy(t *testing.T) {
	for _, name := range []string{"insecure destination", "redirect loop"} {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			client := &http.Client{Transport: auditHTTPTransport(func(request *http.Request) (*http.Response, error) {
				requests.Add(1)
				response := auditHTTPResponse(302, "")
				location := "https://registry.example/loop"
				if name == "insecure destination" {
					location = "http://registry.example/loop"
				}
				response.Header.Set("Location", location)
				return response, nil
			})}
			checker := &checker{client: client, interval: time.Nanosecond}
			var result rdapResponse
			_, err := checker.getJSON(context.Background(), "https://registry.example/start", acceptRDAP, &result)
			if !errors.Is(err, errRedirectPolicy) {
				t.Fatalf("redirect policy error = %v, want %v", err, errRedirectPolicy)
			}
			wantRequests := int32(1)
			if name == "redirect loop" {
				wantRequests = maxRedirects
			}
			if requests.Load() != wantRequests {
				t.Errorf("redirect requests = %d, want %d without retries", requests.Load(), wantRequests)
			}
		})
	}
}

func TestRetryDelayBounds(t *testing.T) {
	testCases := []struct {
		name    string
		value   string
		attempt int
		want    time.Duration
	}{
		{name: "seconds", value: "120", want: 2 * time.Minute},
		{name: "zero", value: "0", want: 0},
		{name: "capped seconds", value: "86401", want: maxRetryAfter},
		// time.Duration(9999999999) * time.Second would overflow to a negative delay.
		{name: "seconds that would overflow", value: "9999999999", want: maxRetryAfter},
		// delay-seconds has no upper bound, so values beyond int64 are valid and saturate.
		{name: "largest int64", value: "9223372036854775807", want: maxRetryAfter},
		{name: "just beyond int64", value: "9223372036854775808", want: maxRetryAfter},
		{name: "far beyond int64", value: "99999999999999999999", want: maxRetryAfter},
		// HTTP/2 field values are not trimmed by the transport.
		{name: "surrounding whitespace", value: " \t120 ", want: 2 * time.Minute},
		{name: "negative falls back to backoff", value: "-5", want: time.Second},
		{name: "signed value falls back to backoff", value: "+120", want: time.Second},
		{name: "fraction falls back to backoff", value: "1.5", want: time.Second},
		{name: "missing header", attempt: 2, want: 4 * time.Second},
		{name: "past date falls back to backoff", value: "Mon, 01 Jan 2001 00:00:00 GMT", want: time.Second},
		{name: "far-future date", value: "Fri, 01 Jan 2300 00:00:00 GMT", want: maxRetryAfter},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryDelay(tc.value, tc.attempt); got != tc.want {
				t.Errorf("retryDelay(%q, %d) = %s, want %s", tc.value, tc.attempt, got, tc.want)
			}
		})
	}
}

func TestGetJSONFailsFastOnLongBackoff(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var slowRequests, fastRequests atomic.Int32
			checker := &checker{interval: time.Nanosecond, client: &http.Client{Transport: auditHTTPTransport(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Host {
				case "slow.example":
					slowRequests.Add(1)
					response := auditHTTPResponse(status, "")
					response.Header.Set("Retry-After", "3600")
					return response, nil
				case "fast.example":
					fastRequests.Add(1)
					return auditHTTPResponse(200, `{"errorCode":0}`), nil
				default:
					return nil, fmt.Errorf("unexpected URL: %s", request.URL)
				}
			})}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			started := time.Now()
			var result rdapResponse
			gotStatus, err := checker.getJSON(ctx, "https://slow.example/domain/a.com", acceptRDAP, &result)
			if !errors.Is(err, errBackoffTooLong) || gotStatus != status || !strings.Contains(err.Error(), "Retry-After 1h0m0s") {
				t.Fatalf("long Retry-After = HTTP %d, %v; want HTTP %d with %v", gotStatus, err, status, errBackoffTooLong)
			}
			// Later domains on the throttled host fail without contacting it again.
			gotStatus, err = checker.getJSON(ctx, "https://slow.example/domain/b.com", acceptRDAP, &result)
			if !errors.Is(err, errBackoffTooLong) || gotStatus != 0 || !strings.Contains(err.Error(), "slow.example") {
				t.Errorf("request during backoff = HTTP %d, %v; want %v naming the host", gotStatus, err, errBackoffTooLong)
			}
			if slowRequests.Load() != 1 {
				t.Errorf("throttled host requests = %d, want 1 without retries", slowRequests.Load())
			}
			// Other hosts are unaffected.
			if gotStatus, err = checker.getJSON(ctx, "https://fast.example/domain/c.com", acceptRDAP, &result); err != nil || gotStatus != 200 || fastRequests.Load() != 1 {
				t.Errorf("unthrottled host = HTTP %d, %v after %d requests", gotStatus, err, fastRequests.Load())
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Errorf("long backoff blocked for %s, want an immediate failure", elapsed)
			}
		})
	}
}

func TestGetJSONDoesNotRetryBackoffAfterRedirect(t *testing.T) {
	var sourceRequests atomic.Int32
	checker := &checker{interval: time.Nanosecond, client: &http.Client{Transport: auditHTTPTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "source.example" {
			return nil, fmt.Errorf("request reached a host that is backing off: %s", request.URL)
		}
		sourceRequests.Add(1)
		response := auditHTTPResponse(http.StatusFound, "")
		response.Header.Set("Location", "https://slow.example/domain/example.com")
		return response, nil
	})}}
	// The destination is backing off because of traffic that did not pass through the source.
	checker.limiterFor("slow.example").deferUntil(time.Now().Add(time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	var result rdapResponse
	_, err := checker.getJSON(ctx, "https://source.example/domain/example.com", acceptRDAP, &result)
	if !errors.Is(err, errBackoffTooLong) || !strings.Contains(err.Error(), "slow.example") {
		t.Errorf("redirect into a backing-off host = %v, want %v naming the destination", err, errBackoffTooLong)
	}
	if sourceRequests.Load() != 1 {
		t.Errorf("source requests = %d, want 1 without retries", sourceRequests.Load())
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("redirect into a backing-off host took %s, want an immediate failure", elapsed)
	}
}

func TestGetJSONBackoffAppliesToRespondingHost(t *testing.T) {
	var throttledRequests, healthyRequests atomic.Int32
	checker := &checker{interval: time.Nanosecond, client: &http.Client{Transport: auditHTTPTransport(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Host {
		case "entry.example":
			// A shared entry redirects each domain to the registry that holds it.
			response := auditHTTPResponse(http.StatusFound, "")
			response.Header.Set("Location", "https://"+request.URL.Query().Get("registry")+"/domain/example.com")
			return response, nil
		case "throttled.example":
			throttledRequests.Add(1)
			response := auditHTTPResponse(http.StatusTooManyRequests, "")
			response.Header.Set("Retry-After", "3600")
			return response, nil
		case "healthy.example":
			healthyRequests.Add(1)
			return auditHTTPResponse(200, `{"errorCode":0}`), nil
		default:
			return nil, fmt.Errorf("unexpected URL: %s", request.URL)
		}
	})}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var result rdapResponse
	_, err := checker.getJSON(ctx, "https://entry.example/domain/a?registry=throttled.example", acceptRDAP, &result)
	if !errors.Is(err, errBackoffTooLong) {
		t.Fatalf("throttled registry = %v, want %v", err, errBackoffTooLong)
	}
	if status, err := checker.getJSON(ctx, "https://entry.example/domain/b?registry=healthy.example", acceptRDAP, &result); err != nil || status != 200 || healthyRequests.Load() != 1 {
		t.Errorf("healthy registry behind the same entry = HTTP %d, %v after %d requests", status, err, healthyRequests.Load())
	}
	// The throttled registry still fails fast without being contacted again.
	if _, err := checker.getJSON(ctx, "https://entry.example/domain/c?registry=throttled.example", acceptRDAP, &result); !errors.Is(err, errBackoffTooLong) || !strings.Contains(err.Error(), "throttled.example") || throttledRequests.Load() != 1 {
		t.Errorf("throttled registry during backoff = %v after %d requests, want %v naming it", err, throttledRequests.Load(), errBackoffTooLong)
	}
}

func TestGetJSONTransportRetryDefersFailedHop(t *testing.T) {
	var entryCalls, brokenCalls []time.Time
	checker := &checker{interval: time.Nanosecond, client: &http.Client{Transport: auditHTTPTransport(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Host {
		case "entry.example":
			entryCalls = append(entryCalls, time.Now())
			response := auditHTTPResponse(http.StatusFound, "")
			response.Header.Set("Location", "https://broken.example/domain/example.com")
			return response, nil
		case "broken.example":
			brokenCalls = append(brokenCalls, time.Now())
			if len(brokenCalls) == 1 {
				return nil, errors.New("connection reset by peer")
			}
			return auditHTTPResponse(200, `{"errorCode":0}`), nil
		default:
			return nil, fmt.Errorf("unexpected URL: %s", request.URL)
		}
	})}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var result rdapResponse
	if status, err := checker.getJSON(ctx, "https://entry.example/domain/example.com", acceptRDAP, &result); err != nil || status != 200 {
		t.Fatalf("retry after a transport error = HTTP %d, %v", status, err)
	}
	if len(entryCalls) != 2 || len(brokenCalls) != 2 {
		t.Fatalf("requests: entry=%d broken=%d, want 2 each", len(entryCalls), len(brokenCalls))
	}
	// The failed hop is held back by the first retry delay, which starts after its failure.
	if gap := brokenCalls[1].Sub(brokenCalls[0]); gap < time.Second {
		t.Errorf("failed host was retried after %s, want its own backoff of at least 1s", gap)
	}
	// The shared entry is not deferred, so other registries behind it stay usable.
	if gap := entryCalls[1].Sub(entryCalls[0]); gap > 500*time.Millisecond {
		t.Errorf("entry was retried after %s, want no backoff for a host that did not fail", gap)
	}
}

func TestHostLimiterLongDelay(t *testing.T) {
	next := time.Now().Add(maxRetryWait + 30*time.Second)
	limiter := &hostLimiter{next: next}
	failFast, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := limiter.wait(failFast, time.Nanosecond); !errors.Is(err, errBackoffTooLong) {
		t.Errorf("wait() beyond the retry limit = %v, want %v", err, errBackoffTooLong)
	}
	// An operator-chosen interval above maxRetryWait is pacing, not a server backoff, so it sleeps.
	sleep, cancelSleep := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelSleep()
	if err := limiter.wait(sleep, maxRetryWait+time.Minute); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("wait() within a long interval = %v, want a sleep ended by the deadline", err)
	}
	if !limiter.next.Equal(next) {
		t.Errorf("failed waits changed the next request time to %s, want %s", limiter.next, next)
	}
}

func TestGetJSONRejectsIncompleteOrOversizedBody(t *testing.T) {
	for _, name := range []string{"read error after valid prefix", "empty body", "exactly at limit", "larger than limit"} {
		t.Run(name, func(t *testing.T) {
			checker := &checker{interval: time.Nanosecond, client: &http.Client{Transport: auditHTTPTransport(func(request *http.Request) (*http.Response, error) {
				response := auditHTTPResponse(404, "")
				prefix := `{"errorCode":404}`
				switch name {
				case "read error after valid prefix":
					response.Body = io.NopCloser(io.MultiReader(strings.NewReader(prefix), auditHTTPReadError{}))
				case "empty body":
					// Keep the empty body from auditHTTPResponse.
				default:
					size := maxJSONBytes
					if name == "larger than limit" {
						size++
					}
					response.Body = io.NopCloser(strings.NewReader(prefix + strings.Repeat(" ", size-len(prefix))))
				}
				return response, nil
			})}}
			var result rdapResponse
			status, err := checker.getJSON(context.Background(), "https://registry.example/domain/example.com", acceptRDAP, &result)
			switch {
			case name == "exactly at limit":
				if err != nil || status != 404 || result.ErrorCode != 404 {
					t.Errorf("valid body at limit = HTTP %d, %+v, %v", status, result, err)
				}
			case name == "empty body":
				if !errors.Is(err, errEmptyBody) || status != 404 {
					t.Errorf("empty body = HTTP %d, %v, want 404 with %v", status, err, errEmptyBody)
				}
			case err == nil || result.ErrorCode != 0:
				t.Errorf("invalid body populated evidence: HTTP %d, %+v, %v", status, result, err)
			}
		})
	}
}

type auditHTTPReadError struct{}

func (auditHTTPReadError) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestCheckRejectsTrailingJSONEvidence(t *testing.T) {
	for _, name := range []string{"RDAP garbage", "RDAP second object", "RDAP truncated", "DNS garbage", "DNS second object", "DNS truncated"} {
		t.Run(name, func(t *testing.T) {
			checker := &checker{
				interval: time.Nanosecond, services: map[string]string{"com": "https://registry.example"},
				resolvers: []string{"https://resolver-a.example/resolve", "https://resolver-b.example/resolve"},
				client: &http.Client{Transport: auditHTTPTransport(func(request *http.Request) (*http.Response, error) {
					status, body := 200, dnsResponse(3)
					isRDAP := request.URL.Host == "registry.example"
					if isRDAP {
						status, body = 404, `{"errorCode":404}`
					}
					if isRDAP == strings.HasPrefix(name, "RDAP") {
						switch {
						case strings.HasSuffix(name, "garbage"):
							body += "garbage"
						case strings.HasSuffix(name, "second object"):
							body += `{}`
						case strings.HasSuffix(name, "truncated"):
							body = strings.TrimSuffix(body, "}")
						}
					}
					return auditHTTPResponse(status, body), nil
				})},
			}
			result := checker.check(context.Background(), &domainEntry{Domain: "example.com"})
			if result.Status != "unknown" {
				t.Errorf("invalid evidence produced %q: %+v", result.Status, result)
			}
		})
	}
}
