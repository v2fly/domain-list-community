package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRegistrationDomain(t *testing.T) {
	testCases := []struct {
		host string
		want string
	}{
		{host: "mail.example.com", want: "example.com"},
		{host: "mail.example.co.uk", want: "example.co.uk"},
		{host: "mail.example.com.cn", want: "example.com.cn"},
		{host: "tenant.github.io", want: "github.io"},
		{host: "tenant.blogspot.com", want: "blogspot.com"},
		{host: "co.uk"},
		{host: "com"},
		{host: "router.local"},
		{host: "onion"},
		{host: "hidden.onion"},
		{host: "sub.hidden.onion"},
		{host: "arpa"},
		{host: "home.arpa"},
		{host: "router.home.arpa"},
		{host: "1.0.0.127.in-addr.arpa"},
		{host: "0.0.0.0.ip6.arpa"},
		{host: "notonion.com", want: "notonion.com"},
		{host: "notarpa.com", want: "notarpa.com"},
		{host: "onion.example.com", want: "example.com"},
		{host: "arpa.example.com", want: "example.com"},
		{host: "127.0.0.1"},
		{host: "::1"},
	}
	for _, tc := range testCases {
		t.Run(tc.host, func(t *testing.T) {
			if got := registrationDomain(tc.host); got != tc.want {
				t.Errorf("registrationDomain(%q) = %q, want %q", tc.host, got, tc.want)
			}
		})
	}
}

func TestLoadInventory(t *testing.T) {
	dataPath := t.TempDir()
	files := map[string]string{
		"first":  "# Source rules\nExample.COM @cn &second # comment\nfull:Mail.Example.COM @ads\ninclude:second\nkeyword:example\nregexp:^example\\.com$\ngoogle\nrouter.local\n",
		"second": "domain: example.com @ads\nfull:api.example.co.uk\nfull:hidden.onion\nrouter.home.arpa\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dataPath, name), []byte(content), 0644); err != nil {
			t.Fatalf("failed to write list %q: %v", name, err)
		}
	}
	data, err := loadInventory(dataPath)
	if err != nil {
		t.Fatalf("loadInventory() got unexpected error: %v", err)
	}
	if got := sortedDomains(data.Domains); !slices.Equal(got, []string{"example.co.uk", "example.com"}) {
		t.Fatalf("registration domains = %v, want example.co.uk and example.com", got)
	}
	if data.LiteralRules != 8 {
		t.Errorf("literal rule count = %d, want 8", data.LiteralRules)
	}
	for kind, count := range map[string]int{"include": 1, "keyword": 1, "regexp": 1, "no_registry_domain": 4} {
		if data.Skipped[kind] != count {
			t.Errorf("skipped %s = %d, want %d", kind, data.Skipped[kind], count)
		}
	}
	sources := data.Domains["example.com"].Sources
	if len(sources) != 3 {
		t.Fatalf("example.com source count = %d, want 3", len(sources))
	}
	if sources[0].File != filepath.ToSlash(filepath.Join(dataPath, "first")) || sources[0].Line != 2 || sources[0].Rule != "Example.COM @cn &second # comment" {
		t.Errorf("first source = %+v, want original rule at first:2", sources[0])
	}
	if sources[1].Line != 3 || sources[2].Line != 1 {
		t.Errorf("source line numbers = %d, %d, want 3, 1", sources[1].Line, sources[2].Line)
	}
}

func TestLoadInventoryRejectsEmptyInput(t *testing.T) {
	if _, err := loadInventory(t.TempDir()); err == nil {
		t.Fatal("loadInventory() accepted an empty data directory")
	}
}

func TestReadHistoryUsesEarliestRegistrationDomainAddition(t *testing.T) {
	domains := map[string]*domainEntry{
		"example.com": {Domain: "example.com"},
		"github.io":   {Domain: "github.io"},
		"missing.org": {Domain: "missing.org"},
	}
	history := strings.Join([]string{
		"DLC-COMMIT 2024-02-01T00:00:00Z",
		"+++ b/data/first",
		"+domain:example.com @cn",
		"+tenant.github.io",
		"DLC-COMMIT 2020-01-01T00:00:00Z",
		"+full:mail.example.com @ads",
		"DLC-COMMIT 2019-01-01T00:00:00Z",
		"+include:missing.org",
		"+regexp:^missing\\.org$",
		"-missing.org",
		"+unrelated.net",
	}, "\n")
	if err := readHistory(strings.NewReader(history), domains); err != nil {
		t.Fatalf("readHistory() got unexpected error: %v", err)
	}
	wantFirstSeen := time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)
	if !domains["example.com"].FirstSeen.Equal(wantFirstSeen) {
		t.Errorf("example.com first seen = %v, want %v", domains["example.com"].FirstSeen, wantFirstSeen)
	}
	if domains["github.io"].FirstSeen.Year() != 2024 {
		t.Errorf("github.io first seen = %v, want tenant rule addition", domains["github.io"].FirstSeen)
	}
	if !domains["missing.org"].FirstSeen.IsZero() {
		t.Errorf("nonliteral or removed rule was used as history: %v", domains["missing.org"].FirstSeen)
	}
}

func TestReadHistoryRejectsInvalidCommitDate(t *testing.T) {
	if err := readHistory(strings.NewReader("DLC-COMMIT invalid\n+example.com\n"), map[string]*domainEntry{}); err == nil {
		t.Fatal("readHistory() accepted an invalid commit date")
	}
}
