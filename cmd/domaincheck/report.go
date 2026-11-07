package main

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func writeReports(outputDir string, data *inventory, results []checkResult) error {
	counts := make(map[string]int)
	missingHistory := 0
	missingRegistration := 0
	for _, result := range results {
		counts[result.Status]++
		if result.FirstSeen.IsZero() {
			missingHistory++
		}
		if result.Status == "registered" && result.Registered.IsZero() {
			missingRegistration++
		}
	}
	report := struct {
		GeneratedAt         time.Time      `json:"generated_at"`
		Commit              string         `json:"commit,omitempty"`
		LiteralRules        int            `json:"literal_rules"`
		RegistrationDomains int            `json:"registration_domains"`
		SkippedRules        map[string]int `json:"skipped_rules"`
		Counts              map[string]int `json:"counts"`
		MissingHistory      int            `json:"missing_history"`
		MissingRegistration int            `json:"missing_registration_date"`
		Results             []checkResult  `json:"results"`
	}{
		GeneratedAt: time.Now().UTC(), Commit: os.Getenv("GITHUB_SHA"),
		LiteralRules: data.LiteralRules, RegistrationDomains: len(results), SkippedRules: data.Skipped,
		Counts: counts, MissingHistory: missingHistory, MissingRegistration: missingRegistration, Results: results,
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode audit report: %w", err)
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outputDir, "report.json"), append(encoded, '\n'), 0644); err != nil {
		return err
	}
	var summary strings.Builder
	fmt.Fprintln(&summary, "# Domain registration audit")
	fmt.Fprintf(&summary, "\nScanned %d literal rules, grouped into %d registration domains.\n", data.LiteralRules, len(results))
	fmt.Fprintln(&summary, "\nThis is an advisory report for manual review. No rules are removed automatically. Registered does not mean ownership is correct. A later registration date suggests a lapse but does not prove a change of owner.")
	fmt.Fprintln(&summary, "\n| Result | Domains |\n| --- | ---: |")
	for _, status := range []string{"registered", "possibly_unregistered", "possibly_reregistered", "unknown", "unsupported"} {
		fmt.Fprintf(&summary, "| %s | %d |\n", status, counts[status])
	}
	fmt.Fprintf(&summary, "\nUnknown and unsupported results are coverage gaps, not evidence of expiry. Registration-date comparison was unavailable for %d registered domains without a registration date, and %d domains without rule history.\n", missingRegistration, missingHistory)
	if counts["unknown"] > 0 {
		fmt.Fprintln(&summary, "\n**Coverage is incomplete.** Inspect `unknown` results in the JSON report before drawing conclusions.")
	}
	fmt.Fprintln(&summary, "\nSkipped rules:")
	skipTypes := make([]string, 0, len(data.Skipped))
	for typ := range data.Skipped {
		skipTypes = append(skipTypes, typ)
	}
	slices.Sort(skipTypes)
	for _, typ := range skipTypes {
		fmt.Fprintf(&summary, "- %s: %d\n", markdownText(typ), data.Skipped[typ])
	}
	fmt.Fprintln(&summary, "\nIncludes are covered through their source files. Keywords and regular expressions cannot be queried as DNS names. TLD-only names and names without an ICANN registration boundary are excluded, along with Tor .onion names and infrastructure .arpa names.")
	fmt.Fprintln(&summary, "\n## Review candidates\n\nA missing registration requires registry RDAP `errorCode: 404` plus NXDOMAIN from both Google and Cloudflare DNS for the registration domain. NOERROR (including NODATA), DNS failures, non-RDAP 404 responses and unsupported registries are not classified as unregistered.")
	fmt.Fprintln(&summary, "\n| Domain | Finding | First in history (UTC) | Registered (UTC) | Source locations |\n| --- | --- | --- | --- | --- |")
	candidates := 0
	for _, result := range results {
		if result.Status != "possibly_unregistered" && result.Status != "possibly_reregistered" {
			continue
		}
		candidates++
		if candidates > 100 {
			continue
		}
		locations := make([]string, 0, 3)
		for _, source := range result.Sources[:min(3, len(result.Sources))] {
			locations = append(locations, fmt.Sprintf("%s:%d", markdownText(source.File), source.Line))
		}
		fmt.Fprintf(&summary, "| %s | %s | %s | %s | %s |\n", markdownText(result.Domain), result.Status, reportDate(result.FirstSeen), reportDate(result.Registered), strings.Join(locations, ", "))
	}
	fmt.Fprintf(&summary, "\n%d review candidates. Summary shows at most 100; download `report.json` for every result, source rule, RDAP URL, registry status and DNS confirmation.\n", candidates)
	fmt.Fprintln(&summary, "\nHistory uses the earliest addition of any rule for the registration domain in this repository, with a 24-hour margin for timestamp differences. Imported lists and registry timestamp changes may need additional verification. DNS is queried only to confirm RDAP-not-found candidates; subdomain availability and live ownership changes without re-registration are outside this audit.")
	if err := os.WriteFile(filepath.Join(outputDir, "summary.md"), []byte(summary.String()), 0644); err != nil {
		return err
	}
	fmt.Printf("Audit reports saved to %s: %d review candidates, %d unknown, %d unsupported\n", outputDir, candidates, counts["unknown"], counts["unsupported"])
	if counts["registered"]+counts["possibly_unregistered"]+counts["possibly_reregistered"] == 0 {
		return fmt.Errorf("no conclusive registry responses; reports saved with coverage gaps")
	}
	return nil
}

func markdownText(value string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ", "\r", " ").Replace(html.EscapeString(value))
}

func reportDate(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.UTC().Format(time.DateOnly)
}
