package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/v2fly/domain-list-community/internal/dlc"
	"golang.org/x/net/publicsuffix"
)

type source struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Rule string `json:"rule"`
}

type domainEntry struct {
	Domain    string    `json:"domain"`
	Sources   []source  `json:"sources"`
	FirstSeen time.Time `json:"first_seen"`
}

type inventory struct {
	Domains      map[string]*domainEntry
	LiteralRules int
	Skipped      map[string]int
}

func literalDomain(line string) (string, string) {
	line, _, _ = strings.Cut(line, "#")
	line = strings.TrimSpace(line)
	if line == "" {
		return "", ""
	}
	typ, value, specified := strings.Cut(strings.ToLower(line), ":")
	if !specified {
		return strings.Fields(typ)[0], ""
	}
	switch typ {
	case dlc.RuleTypeDomain, dlc.RuleTypeFullDomain:
		fields := strings.Fields(value)
		if len(fields) == 0 {
			return "", "empty_domain"
		}
		return fields[0], ""
	default:
		return "", typ
	}
}

func registrationDomain(host string) string {
	// PSL inclusion does not make Tor names or infrastructure names registrable.
	if host == "onion" || strings.HasSuffix(host, ".onion") || host == "arpa" || strings.HasSuffix(host, ".arpa") {
		return ""
	}
	if net.ParseIP(host) != nil {
		return ""
	}
	// Ignore private PSL boundaries: a tenant of github.io is not a registry object.
	for candidate := host; candidate != ""; {
		suffix, icann := publicsuffix.PublicSuffix(candidate)
		if icann {
			if host == suffix {
				return ""
			}
			prefix := strings.TrimSuffix(host, "."+suffix)
			labels := strings.Split(prefix, ".")
			return labels[len(labels)-1] + "." + suffix
		}
		_, rest, found := strings.Cut(candidate, ".")
		if !found {
			return ""
		}
		candidate = rest
	}
	return ""
}

func loadInventory(dataPath string) (*inventory, error) {
	result := &inventory{Domains: make(map[string]*domainEntry), Skipped: make(map[string]int)}
	err := filepath.WalkDir(dataPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			host, skip := literalDomain(scanner.Text())
			if skip != "" {
				result.Skipped[skip]++
				continue
			}
			if host == "" {
				continue
			}
			result.LiteralRules++
			domain := registrationDomain(host)
			if domain == "" {
				result.Skipped["no_registry_domain"]++
				continue
			}
			if result.Domains[domain] == nil {
				result.Domains[domain] = &domainEntry{Domain: domain}
			}
			result.Domains[domain].Sources = append(result.Domains[domain].Sources, source{
				File: filepath.ToSlash(path), Line: lineNumber, Rule: strings.TrimSpace(scanner.Text()),
			})
		}
		return scanner.Err()
	})
	if err != nil {
		return nil, err
	}
	if len(result.Domains) == 0 {
		return nil, fmt.Errorf("no registrable domains found in %q", dataPath)
	}
	return result, nil
}

func readHistory(reader io.Reader, domains map[string]*domainEntry) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var committed time.Time
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "DLC-COMMIT ") {
			parsed, err := time.Parse(time.RFC3339, strings.TrimPrefix(line, "DLC-COMMIT "))
			if err != nil {
				return err
			}
			committed = parsed
			continue
		}
		if !strings.HasPrefix(line, "+") || strings.HasPrefix(line, "+++") || committed.IsZero() {
			continue
		}
		host, _ := literalDomain(strings.TrimPrefix(line, "+"))
		if host == "" {
			continue
		}
		entry := domains[registrationDomain(host)]
		if entry != nil && (entry.FirstSeen.IsZero() || committed.Before(entry.FirstSeen)) {
			entry.FirstSeen = committed
		}
	}
	return scanner.Err()
}

func loadHistory(ctx context.Context, dataPath string, domains map[string]*domainEntry) error {
	shallow, err := exec.CommandContext(ctx, "git", "rev-parse", "--is-shallow-repository").Output()
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(shallow)) != "false" {
		return fmt.Errorf("complete git history is required; checkout with fetch-depth: 0")
	}
	command := exec.CommandContext(ctx, "git", "log", "--format=DLC-COMMIT %cI", "--no-renames", "--diff-filter=AM", "--unified=0", "-p", "--", dataPath)
	command.Stderr = os.Stderr
	pipe, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return err
	}
	readErr := readHistory(pipe, domains)
	if readErr != nil {
		_ = command.Process.Kill()
	}
	if err := command.Wait(); err != nil {
		return err
	}
	return readErr
}

func sortedDomains(domains map[string]*domainEntry) []string {
	names := make([]string, 0, len(domains))
	for name := range domains {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
