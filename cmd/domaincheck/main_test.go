package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type bootstrapTransport func(*http.Request) (*http.Response, error)

func (transport bootstrapTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestAuditDomainsPreservesReportsWhenBootstrapTimesOut(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			firstSeen := time.Date(2020, time.July, 18, 0, 0, 0, 0, time.UTC)
			data := &inventory{
				Domains: map[string]*domainEntry{
					"zebra.com": {Domain: "zebra.com", FirstSeen: firstSeen, Sources: []source{{File: "data/zebra", Line: 8, Rule: "full:www.zebra.com"}}},
					"alpha.com": {Domain: "alpha.com", FirstSeen: firstSeen, Sources: []source{{File: "data/alpha", Line: 3, Rule: "alpha.com @cn"}}},
				},
				LiteralRules: 2, Skipped: map[string]int{"keyword": 1},
			}
			var requests atomic.Int32
			client := &http.Client{Transport: bootstrapTransport(func(request *http.Request) (*http.Response, error) {
				requests.Add(1)
				if request.URL.String() != bootstrapURL {
					t.Errorf("unexpected request before bootstrap succeeded: %s", request.URL)
				}
				return &http.Response{
					StatusCode: status,
					Header:     http.Header{"Retry-After": []string{"3600"}},
					Body:       io.NopCloser(strings.NewReader("")),
					Request:    request,
				}, nil
			})}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			outputDir := t.TempDir()
			err := auditDomains(ctx, data, client, time.Nanosecond, 2, outputDir)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("auditDomains() error = %v, want bootstrap deadline preserved", err)
			}
			if !strings.Contains(err.Error(), "load RDAP bootstrap") {
				t.Errorf("audit error does not identify bootstrap failure: %v", err)
			}
			if requests.Load() != 1 {
				t.Errorf("bootstrap requests = %d, want one request followed by Retry-After deadline", requests.Load())
			}
			encoded, err := os.ReadFile(filepath.Join(outputDir, "report.json"))
			if err != nil {
				t.Fatalf("bootstrap failure did not save report: %v", err)
			}
			var report struct {
				Counts       map[string]int `json:"counts"`
				LiteralRules int            `json:"literal_rules"`
				SkippedRules map[string]int `json:"skipped_rules"`
				Results      []struct {
					Domain    string    `json:"domain"`
					Status    string    `json:"status"`
					Detail    string    `json:"detail"`
					FirstSeen time.Time `json:"first_seen"`
					Sources   []source  `json:"sources"`
				} `json:"results"`
			}
			if err := json.Unmarshal(encoded, &report); err != nil {
				t.Fatalf("decode bootstrap failure report: %v", err)
			}
			if report.Counts["unknown"] != 2 || report.Counts["unsupported"] != 0 || report.LiteralRules != 2 || report.SkippedRules["keyword"] != 1 {
				t.Errorf("bootstrap failure coverage was not preserved: %+v", report)
			}
			if len(report.Results) != 2 {
				t.Fatalf("bootstrap report results = %d, want every inventoried domain", len(report.Results))
			}
			for index, name := range []string{"alpha.com", "zebra.com"} {
				result := report.Results[index]
				if result.Domain != name || result.Status != "unknown" || !strings.Contains(result.Detail, "load RDAP bootstrap") || !strings.Contains(result.Detail, "deadline exceeded") {
					t.Errorf("bootstrap failure result = %+v, want sorted unknown coverage with cause", result)
				}
				if !result.FirstSeen.Equal(firstSeen) || !reflect.DeepEqual(result.Sources, data.Domains[name].Sources) {
					t.Errorf("bootstrap failure lost inventory evidence: %+v", result)
				}
			}
			summary, err := os.ReadFile(filepath.Join(outputDir, "summary.md"))
			if err != nil {
				t.Fatalf("bootstrap failure did not save summary: %v", err)
			}
			if !strings.Contains(string(summary), "Coverage is incomplete") || !strings.Contains(string(summary), "| unknown | 2 |") {
				t.Error("bootstrap failure summary did not disclose unknown coverage")
			}
		})
	}
}

func TestAuditDomainsPreservesBootstrapAndReportWriteErrors(t *testing.T) {
	bootstrapErr := errors.New("bootstrap transport unavailable")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &http.Client{Transport: bootstrapTransport(func(request *http.Request) (*http.Response, error) {
		cancel()
		return nil, bootstrapErr
	})}
	data := &inventory{Domains: map[string]*domainEntry{"example.com": {Domain: "example.com"}}, LiteralRules: 1, Skipped: make(map[string]int)}
	outputFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(outputFile, []byte("occupied"), 0644); err != nil {
		t.Fatal(err)
	}
	err := auditDomains(ctx, data, client, time.Nanosecond, 1, outputFile)
	if !errors.Is(err, bootstrapErr) {
		t.Errorf("auditDomains() error = %v, want original bootstrap transport error", err)
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Path != outputFile {
		t.Errorf("auditDomains() error = %v, want report write path error as well", err)
	}
}
