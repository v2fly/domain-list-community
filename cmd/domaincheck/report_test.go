package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWriteReportsPreservesEvidenceAndCoverage(t *testing.T) {
	outputDir := filepath.Join(t.TempDir(), "audit")
	firstSeen := time.Date(2020, time.July, 18, 13, 5, 41, 0, time.UTC)
	registered := time.Date(2026, time.October, 30, 8, 0, 0, 0, time.UTC)
	sources := []source{
		{File: "data/apple", Line: 8, Rule: "domain:example.com @cn"},
		{File: "data/beats", Line: 12, Rule: "full:www.example.com"},
		{File: "<data>|sample", Line: 7, Rule: "example.com"},
		{File: "data/other", Line: 3, Rule: "example.com &apple"},
	}
	results := []checkResult{
		{
			domainEntry: &domainEntry{Domain: "example.com", Sources: sources, FirstSeen: firstSeen},
			Status:      "possibly_reregistered", RDAPURL: "https://registry.example/domain/example.com",
			HTTPStatus: 200, Registered: registered, RegistryStatus: []string{"active"},
			Detail: "Review registration history",
		},
		{
			domainEntry: &domainEntry{Domain: "example.net", FirstSeen: firstSeen},
			Status:      "registered",
		},
		{
			domainEntry: &domainEntry{Domain: "example.org"}, Status: "unknown",
			HTTPStatus: 403, Detail: "HTTP 403",
		},
	}
	data := &inventory{LiteralRules: 6, Skipped: map[string]int{"keyword": 2}}
	if err := writeReports(outputDir, data, results); err != nil {
		t.Fatalf("writeReports() got unexpected error: %v", err)
	}
	encoded, err := os.ReadFile(filepath.Join(outputDir, "report.json"))
	if err != nil {
		t.Fatalf("failed to read report: %v", err)
	}
	var report struct {
		GeneratedAt         time.Time      `json:"generated_at"`
		LiteralRules        int            `json:"literal_rules"`
		RegistrationDomains int            `json:"registration_domains"`
		SkippedRules        map[string]int `json:"skipped_rules"`
		Counts              map[string]int `json:"counts"`
		MissingHistory      int            `json:"missing_history"`
		MissingRegistration int            `json:"missing_registration_date"`
		Results             []struct {
			Domain         string    `json:"domain"`
			Sources        []source  `json:"sources"`
			FirstSeen      time.Time `json:"first_seen"`
			Status         string    `json:"status"`
			RDAPURL        string    `json:"rdap_url"`
			HTTPStatus     int       `json:"http_status"`
			Registered     time.Time `json:"registered"`
			RegistryStatus []string  `json:"registry_status"`
			Detail         string    `json:"detail"`
		} `json:"results"`
	}
	if err := json.Unmarshal(encoded, &report); err != nil {
		t.Fatalf("failed to decode report: %v", err)
	}
	if report.GeneratedAt.IsZero() || report.LiteralRules != 6 || report.RegistrationDomains != 3 {
		t.Errorf("report metadata = %v, %d rules, %d domains", report.GeneratedAt, report.LiteralRules, report.RegistrationDomains)
	}
	wantCounts := map[string]int{"possibly_reregistered": 1, "registered": 1, "unknown": 1}
	if !reflect.DeepEqual(report.Counts, wantCounts) {
		t.Errorf("report counts = %v, want %v", report.Counts, wantCounts)
	}
	if report.SkippedRules["keyword"] != 2 || report.MissingHistory != 1 || report.MissingRegistration != 1 {
		t.Errorf("coverage counts = skipped %v, history %d, registration %d", report.SkippedRules, report.MissingHistory, report.MissingRegistration)
	}
	if len(report.Results) != len(results) {
		t.Fatalf("report has %d results, want %d", len(report.Results), len(results))
	}
	first := report.Results[0]
	if first.Domain != "example.com" || !reflect.DeepEqual(first.Sources, sources) || !first.FirstSeen.Equal(firstSeen) || !first.Registered.Equal(registered) {
		t.Errorf("report did not preserve source and date evidence: %+v", first)
	}
	if first.Status != "possibly_reregistered" || first.RDAPURL != results[0].RDAPURL || first.HTTPStatus != 200 || !reflect.DeepEqual(first.RegistryStatus, []string{"active"}) || first.Detail != results[0].Detail {
		t.Errorf("report did not preserve registry evidence: %+v", first)
	}
	summary, err := os.ReadFile(filepath.Join(outputDir, "summary.md"))
	if err != nil {
		t.Fatalf("failed to read summary: %v", err)
	}
	for _, want := range []string{"Coverage is incomplete", "| unknown | 1 |", "data/apple:8", "&lt;data&gt;\\|sample:7", "2020-07-18", "2026-10-30"} {
		if !strings.Contains(string(summary), want) {
			t.Errorf("summary is missing %q", want)
		}
	}
	if strings.Contains(string(summary), "data/other:3") {
		t.Error("summary contains more than three source locations for one candidate")
	}
}

func TestWriteReportsLimitsSummaryOnly(t *testing.T) {
	outputDir := t.TempDir()
	results := make([]checkResult, 101)
	for index := range results {
		results[index] = checkResult{
			domainEntry: &domainEntry{Domain: fmt.Sprintf("candidate%03d.com", index)},
			Status:      "possibly_unregistered", HTTPStatus: 404,
			DNS: []string{"NXDOMAIN", "NXDOMAIN"},
		}
	}
	data := &inventory{LiteralRules: len(results), Skipped: make(map[string]int)}
	if err := writeReports(outputDir, data, results); err != nil {
		t.Fatalf("writeReports() got unexpected error: %v", err)
	}
	summary, err := os.ReadFile(filepath.Join(outputDir, "summary.md"))
	if err != nil {
		t.Fatalf("failed to read summary: %v", err)
	}
	if !strings.Contains(string(summary), "candidate099.com") || strings.Contains(string(summary), "candidate100.com") {
		t.Error("summary did not stop after the first 100 candidates")
	}
	if !strings.Contains(string(summary), "101 review candidates") {
		t.Error("summary did not report the complete candidate count")
	}
	encoded, err := os.ReadFile(filepath.Join(outputDir, "report.json"))
	if err != nil {
		t.Fatalf("failed to read report: %v", err)
	}
	var report struct {
		Results []struct {
			Domain string   `json:"domain"`
			DNS    []string `json:"dns"`
		} `json:"results"`
	}
	if err := json.Unmarshal(encoded, &report); err != nil {
		t.Fatalf("failed to decode report: %v", err)
	}
	if len(report.Results) != 101 {
		t.Fatalf("report has %d results, want 101", len(report.Results))
	}
	if report.Results[100].Domain != "candidate100.com" || !reflect.DeepEqual(report.Results[100].DNS, []string{"NXDOMAIN", "NXDOMAIN"}) {
		t.Errorf("report lost candidate or DNS evidence: %+v", report.Results[100])
	}
}

func TestWriteReportsPreservesInconclusiveResults(t *testing.T) {
	outputDir := t.TempDir()
	data := &inventory{LiteralRules: 2, Skipped: make(map[string]int)}
	results := []checkResult{
		{domainEntry: &domainEntry{Domain: "example.com"}, Status: "unknown", Detail: "query failed"},
		{domainEntry: &domainEntry{Domain: "example.cn"}, Status: "unsupported"},
	}
	if err := writeReports(outputDir, data, results); err == nil {
		t.Fatal("writeReports() = nil, want error for no conclusive responses")
	}
	for _, name := range []string{"report.json", "summary.md"} {
		content, err := os.ReadFile(filepath.Join(outputDir, name))
		if err != nil {
			t.Fatalf("inconclusive audit did not preserve %s: %v", name, err)
		}
		if len(content) == 0 {
			t.Errorf("inconclusive audit wrote an empty %s", name)
		}
	}
	encoded, err := os.ReadFile(filepath.Join(outputDir, "report.json"))
	if err != nil {
		t.Fatalf("failed to read report: %v", err)
	}
	var report struct {
		Counts map[string]int `json:"counts"`
	}
	if err := json.Unmarshal(encoded, &report); err != nil {
		t.Fatalf("failed to decode report: %v", err)
	}
	if report.Counts["unknown"] != 1 || report.Counts["unsupported"] != 1 || report.Counts["possibly_unregistered"] != 0 {
		t.Errorf("inconclusive report counts = %v", report.Counts)
	}
}
