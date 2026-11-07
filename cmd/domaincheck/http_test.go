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
	var sourceRequests, destinationRequests, policyCalls atomic.Int32
	client := &http.Client{
		Timeout: 50 * time.Millisecond,
		CheckRedirect: func(request *http.Request, previous []*http.Request) error {
			policyCalls.Add(1)
			if request.URL.Host != "destination.example" || len(previous) != 1 || request.Header.Get("Accept") == "" {
				t.Errorf("unexpected redirect policy input: %s, %d previous requests, headers %v", request.URL, len(previous), request.Header)
			}
			return nil
		},
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
	status, err := checker.getJSON(ctx, "https://source.example/domain/example.com", &result)
	if err != nil || status != 404 || result.ErrorCode != 404 {
		t.Fatalf("paced redirect = HTTP %d, %+v, %v", status, result, err)
	}
	if sourceRequests.Load() != 1 || destinationRequests.Load() != 1 || policyCalls.Load() != 1 {
		t.Errorf("redirect consumed retries: source=%d destination=%d policy=%d", sourceRequests.Load(), destinationRequests.Load(), policyCalls.Load())
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
	status, err := checker.getJSON(context.Background(), server.URL+"/start", &result)
	if err != nil || status != 200 || result.ErrorCode != 404 || requests.Load() != 3 {
		t.Errorf("same-host TLS redirects = HTTP %d, %+v, %v, %d requests", status, result, err, requests.Load())
	}
}

func TestGetJSONEnforcesRedirectPolicy(t *testing.T) {
	for _, name := range []string{"insecure destination", "redirect loop", "caller rejects redirect", "caller keeps response"} {
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
			if name == "caller rejects redirect" {
				client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("caller policy") }
			}
			if name == "caller keeps response" {
				client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			}
			checker := &checker{client: client, interval: time.Nanosecond}
			var result rdapResponse
			status, err := checker.getJSON(context.Background(), "https://registry.example/start", &result)
			if err == nil {
				t.Fatal("redirect policy failure was accepted as JSON evidence")
			}
			wantRequests := int32(1)
			if name == "redirect loop" {
				wantRequests = 10
			}
			if requests.Load() != wantRequests {
				t.Errorf("redirect requests = %d, want %d without retries", requests.Load(), wantRequests)
			}
			if name == "caller keeps response" && status != http.StatusFound {
				t.Errorf("caller-retained response status = %d, want 302", status)
			}
		})
	}
}

func TestGetJSONRejectsIncompleteOrOversizedBody(t *testing.T) {
	for _, name := range []string{"read error after valid prefix", "exactly at limit", "larger than limit"} {
		t.Run(name, func(t *testing.T) {
			checker := &checker{interval: time.Nanosecond, client: &http.Client{Transport: auditHTTPTransport(func(request *http.Request) (*http.Response, error) {
				response := auditHTTPResponse(404, "")
				prefix := `{"errorCode":404}`
				if name == "read error after valid prefix" {
					response.Body = io.NopCloser(io.MultiReader(strings.NewReader(prefix), auditHTTPReadError{}))
				} else {
					size := maxJSONBytes
					if name == "larger than limit" {
						size++
					}
					response.Body = io.NopCloser(strings.NewReader(prefix + strings.Repeat(" ", size-len(prefix))))
				}
				return response, nil
			})}}
			var result rdapResponse
			status, err := checker.getJSON(context.Background(), "https://registry.example/domain/example.com", &result)
			if name == "exactly at limit" {
				if err != nil || status != 404 || result.ErrorCode != 404 {
					t.Errorf("valid body at limit = HTTP %d, %+v, %v", status, result, err)
				}
			} else if err == nil || result.ErrorCode != 0 {
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
