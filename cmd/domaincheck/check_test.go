package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckRegistryAndDNS(t *testing.T) {
	firstSeen := time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)
	registered := `{"objectClassName":"domain","ldhName":"example.com","events":[{"eventAction":"registration","eventDate":"2000-01-01T00:00:00Z"}]}`
	testCases := []struct {
		name         string
		rdapStatus   int
		rdapBody     string
		dnsBodies    []string
		wantStatus   string
		wantDNS      []string
		wantRegistry []string
	}{
		{name: "registered", rdapStatus: 200, rdapBody: registered, wantStatus: "registered"},
		{name: "registered on hold", rdapStatus: 200, rdapBody: `{"objectClassName":"domain","ldhName":"example.com","status":["server hold"]}`, wantStatus: "registered", wantRegistry: []string{"server hold"}},
		{name: "two NXDOMAIN confirmations", rdapStatus: 404, rdapBody: `{"errorCode":404}`, dnsBodies: []string{dnsResponse(3), dnsResponse(3)}, wantStatus: "possibly_unregistered", wantDNS: []string{"NXDOMAIN", "NXDOMAIN"}},
		{name: "NOERROR without answers is not NXDOMAIN", rdapStatus: 404, rdapBody: `{"errorCode":404}`, dnsBodies: []string{dnsResponse(3), dnsResponse(0)}, wantStatus: "unknown", wantDNS: []string{"NXDOMAIN", "NOERROR"}},
		{name: "SERVFAIL is not NXDOMAIN", rdapStatus: 404, rdapBody: `{"errorCode":404}`, dnsBodies: []string{dnsResponse(2), dnsResponse(3)}, wantStatus: "unknown", wantDNS: []string{"RCODE_2", "NXDOMAIN"}},
		{name: "missing DNS status", rdapStatus: 404, rdapBody: `{"errorCode":404}`, dnsBodies: []string{`{"Question":[{"name":"example.com.","type":2}]}`, dnsResponse(3)}, wantStatus: "unknown", wantDNS: []string{"unknown", "NXDOMAIN"}},
		{name: "different DNS question", rdapStatus: 404, rdapBody: `{"errorCode":404}`, dnsBodies: []string{`{"Status":3,"Question":[{"name":"other.com.","type":2}]}`, dnsResponse(3)}, wantStatus: "unknown", wantDNS: []string{"unknown", "NXDOMAIN"}},
		{name: "empty 404 body with two NXDOMAIN confirmations", rdapStatus: 404, rdapBody: "", dnsBodies: []string{dnsResponse(3), dnsResponse(3)}, wantStatus: "possibly_unregistered", wantDNS: []string{"NXDOMAIN", "NXDOMAIN"}},
		{name: "blank 404 body with two NXDOMAIN confirmations", rdapStatus: 404, rdapBody: "\r\n", dnsBodies: []string{dnsResponse(3), dnsResponse(3)}, wantStatus: "possibly_unregistered", wantDNS: []string{"NXDOMAIN", "NXDOMAIN"}},
		{name: "empty 404 body without two NXDOMAIN confirmations", rdapStatus: 404, rdapBody: "", dnsBodies: []string{dnsResponse(3), dnsResponse(0)}, wantStatus: "unknown", wantDNS: []string{"NXDOMAIN", "NOERROR"}},
		{name: "empty 200 body", rdapStatus: 200, rdapBody: "", wantStatus: "unknown"},
		{name: "HTML 404", rdapStatus: 404, rdapBody: "<html>Not found</html>", wantStatus: "unknown"},
		{name: "404 without RDAP error", rdapStatus: 404, rdapBody: `{}`, wantStatus: "unknown"},
		{name: "RDAP error disagrees with HTTP", rdapStatus: 404, rdapBody: `{"errorCode":400}`, wantStatus: "unknown"},
		{name: "RDAP domain mismatch", rdapStatus: 200, rdapBody: `{"objectClassName":"domain","ldhName":"other.com"}`, wantStatus: "unknown"},
		{name: "RDAP object class mismatch", rdapStatus: 200, rdapBody: `{"objectClassName":"entity","ldhName":"example.com"}`, wantStatus: "unknown"},
		{name: "invalid registration date", rdapStatus: 200, rdapBody: `{"objectClassName":"domain","ldhName":"example.com","events":[{"eventAction":"registration","eventDate":"invalid"}]}`, wantStatus: "unknown"},
		{name: "registration exactly 24 hours later", rdapStatus: 200, rdapBody: `{"objectClassName":"domain","ldhName":"example.com","events":[{"eventAction":"registration","eventDate":"2020-01-02T00:00:00Z"}]}`, wantStatus: "registered"},
		{name: "registration beyond timestamp margin", rdapStatus: 200, rdapBody: `{"objectClassName":"domain","ldhName":"EXAMPLE.COM.","events":[{"eventAction":"registration","eventDate":"2020-01-02T00:00:01Z"}]}`, wantStatus: "possibly_reregistered"},
		{name: "renewal reported as reregistration", rdapStatus: 200, rdapBody: `{"objectClassName":"domain","ldhName":"example.com","events":[{"eventAction":"registration","eventDate":"2000-01-01T00:00:00Z"},{"eventAction":"reregistration","eventDate":"2024-01-01T00:00:00Z"}]}`, wantStatus: "registered"},
		{name: "earliest registration event", rdapStatus: 200, rdapBody: `{"objectClassName":"domain","ldhName":"example.com","events":[{"eventAction":"registration","eventDate":"2024-01-01T00:00:00Z"},{"eventAction":"registration","eventDate":"2000-01-01T00:00:00Z"}]}`, wantStatus: "registered"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/rdap/domain/example.com" {
					writer.Header().Set("Content-Type", "application/rdap+json")
					writer.WriteHeader(tc.rdapStatus)
					fmt.Fprint(writer, tc.rdapBody)
					return
				}
				index := slices.Index([]string{"/dns-a", "/dns-b"}, request.URL.Path)
				if index < 0 || index >= len(tc.dnsBodies) {
					t.Errorf("unexpected DNS request: %s", request.URL)
					writer.WriteHeader(http.StatusBadRequest)
					return
				}
				if request.URL.Query().Get("name") != "example.com." || request.URL.Query().Get("type") != "NS" {
					t.Errorf("DNS query = %s, want registration domain NS question", request.URL.RawQuery)
				}
				if request.Header.Get("Accept") != "application/dns-json" {
					t.Errorf("DNS Accept header = %q, want application/dns-json", request.Header.Get("Accept"))
				}
				writer.Header().Set("Content-Type", "application/dns-json")
				fmt.Fprint(writer, tc.dnsBodies[index])
			}))
			defer server.Close()
			checker := &checker{
				client: server.Client(), services: map[string]string{"com": server.URL + "/rdap"},
				interval: time.Nanosecond, resolvers: []string{server.URL + "/dns-a", server.URL + "/dns-b"},
			}
			result := checker.check(context.Background(), &domainEntry{Domain: "example.com", FirstSeen: firstSeen})
			if result.Status != tc.wantStatus {
				t.Errorf("check() status = %q, want %q; detail: %s", result.Status, tc.wantStatus, result.Detail)
			}
			if !slices.Equal(result.DNS, tc.wantDNS) {
				t.Errorf("DNS evidence = %v, want %v", result.DNS, tc.wantDNS)
			}
			if !slices.Equal(result.RegistryStatus, tc.wantRegistry) {
				t.Errorf("registry status = %v, want %v", result.RegistryStatus, tc.wantRegistry)
			}
			if result.HTTPStatus != tc.rdapStatus || result.RDAPURL != server.URL+"/rdap/domain/example.com" {
				t.Errorf("registry evidence = HTTP %d %q, want original registry response", result.HTTPStatus, result.RDAPURL)
			}
		})
	}
}

func dnsResponse(status int) string {
	return fmt.Sprintf(`{"Status":%d,"Question":[{"name":"example.com.","type":2}]}`, status)
}

func TestCheckAcceptsEmpty404EndedByConnectionClose(t *testing.T) {
	// Verisign sends its empty 404 without Content-Length or chunked encoding, so the body ends at EOF.
	const closeDelay = 50 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/rdap/domain/example.com" {
			writer.Header().Set("Content-Type", "application/dns-json")
			fmt.Fprint(writer, dnsResponse(3))
			return
		}
		connection, buffer, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack RDAP connection: %v", err)
			return
		}
		defer connection.Close()
		fmt.Fprint(buffer, "HTTP/1.1 404 Not Found\r\nContent-Type: application/rdap+json\r\n\r\n")
		if err := buffer.Flush(); err != nil {
			t.Errorf("write RDAP headers: %v", err)
		}
		time.Sleep(closeDelay)
	}))
	defer server.Close()
	checker := &checker{
		client: server.Client(), services: map[string]string{"com": server.URL + "/rdap"},
		interval: time.Nanosecond, resolvers: []string{server.URL + "/dns-a", server.URL + "/dns-b"},
	}
	started := time.Now()
	result := checker.check(context.Background(), &domainEntry{Domain: "example.com"})
	// The body is complete only at EOF; returning earlier would skip a body that follows the headers.
	if elapsed := time.Since(started); elapsed < closeDelay {
		t.Errorf("check() returned after %s, before the server closed the connection", elapsed)
	}
	if result.Status != "possibly_unregistered" || result.HTTPStatus != http.StatusNotFound || !slices.Equal(result.DNS, []string{"NXDOMAIN", "NXDOMAIN"}) {
		t.Errorf("connection-closed empty 404 = %+v, want possibly_unregistered after DNS confirmation", result)
	}
}

func TestCheckRequiresHistoryForReregistration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, `{"objectClassName":"domain","ldhName":"example.com","events":[{"eventAction":"registration","eventDate":"2024-01-01T00:00:00Z"}]}`)
	}))
	defer server.Close()
	checker := &checker{client: server.Client(), services: map[string]string{"com": server.URL}, interval: time.Nanosecond}
	result := checker.check(context.Background(), &domainEntry{Domain: "example.com"})
	if result.Status != "registered" || result.Registered.IsZero() {
		t.Errorf("check without history = %+v, want registered with date and no ownership inference", result)
	}
}

func TestCheckUnsupportedRegistry(t *testing.T) {
	checker := &checker{services: map[string]string{}}
	result := checker.check(context.Background(), &domainEntry{Domain: "example.com"})
	if result.Status != "unsupported" || result.RDAPURL != "" {
		t.Errorf("check unsupported registry = %+v, want unsupported without a network request", result)
	}
}

func TestCheckDeadlineIsUnknown(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	checker := &checker{client: &http.Client{}, services: map[string]string{"com": "https://registry.invalid"}, interval: time.Nanosecond}
	result := checker.check(ctx, &domainEntry{Domain: "example.com"})
	if result.Status != "unknown" || !strings.Contains(result.Detail, "deadline exceeded") {
		t.Errorf("expired audit deadline = %+v, want unknown with deadline detail", result)
	}
}

func TestCheckRetriesTemporaryRegistryFailures(t *testing.T) {
	testCases := []struct {
		name          string
		failureStatus int
		failures      int32
		wantRequests  int32
		wantHTTP      int
		wantStatus    string
	}{
		{name: "rate limit then success", failureStatus: http.StatusTooManyRequests, failures: 1, wantRequests: 2, wantHTTP: http.StatusOK, wantStatus: "registered"},
		{name: "unavailable then success", failureStatus: http.StatusServiceUnavailable, failures: 1, wantRequests: 2, wantHTTP: http.StatusOK, wantStatus: "registered"},
		{name: "persistent rate limit", failureStatus: http.StatusTooManyRequests, failures: 3, wantRequests: 3, wantHTTP: http.StatusTooManyRequests, wantStatus: "unknown"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if requests.Add(1) <= tc.failures {
					writer.Header().Set("Retry-After", "0")
					writer.WriteHeader(tc.failureStatus)
					return
				}
				fmt.Fprint(writer, `{"objectClassName":"domain","ldhName":"example.com"}`)
			}))
			defer server.Close()
			checker := &checker{client: server.Client(), services: map[string]string{"com": server.URL}, interval: time.Nanosecond}
			result := checker.check(context.Background(), &domainEntry{Domain: "example.com"})
			if requests.Load() != tc.wantRequests {
				t.Errorf("registry request count = %d, want %d", requests.Load(), tc.wantRequests)
			}
			if result.Status != tc.wantStatus || result.HTTPStatus != tc.wantHTTP {
				t.Errorf("registry result = %q HTTP %d, want %q HTTP %d; detail: %s", result.Status, result.HTTPStatus, tc.wantStatus, tc.wantHTTP, result.Detail)
			}
		})
	}
}
