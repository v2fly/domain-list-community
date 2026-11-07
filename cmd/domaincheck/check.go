package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const bootstrapURL = "https://data.iana.org/rdap/dns.json"

const (
	statusRegistered           = "registered"
	statusPossiblyUnregistered = "possibly_unregistered"
	statusPossiblyReregistered = "possibly_reregistered"
	statusUnknown              = "unknown"
	statusUnsupported          = "unsupported"
)

// DNS RR type and response codes used by the JSON DNS-over-HTTPS APIs.
const (
	dnsTypeNS        = 2
	dnsRcodeNoError  = 0
	dnsRcodeNXDomain = 3
)

type checkResult struct {
	*domainEntry
	Status         string    `json:"status"`
	RDAPURL        string    `json:"rdap_url,omitempty"`
	HTTPStatus     int       `json:"http_status,omitempty"`
	Registered     time.Time `json:"registered,omitzero"`
	DNS            []string  `json:"dns,omitempty"`
	RegistryStatus []string  `json:"registry_status,omitempty"`
	Detail         string    `json:"detail,omitempty"`
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

type checker struct {
	client    *http.Client
	services  map[string]string
	interval  time.Duration
	limiters  sync.Map
	resolvers []string
}

func newChecker(ctx context.Context, client *http.Client, interval time.Duration) (*checker, error) {
	result := &checker{
		client: client, interval: interval, services: make(map[string]string),
		resolvers: []string{"https://dns.google/resolve", "https://cloudflare-dns.com/dns-query"},
	}
	var bootstrap struct {
		Services [][][]string `json:"services"`
	}
	status, err := result.getJSON(ctx, bootstrapURL, acceptJSON, &bootstrap)
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
	status, err := checker.getJSON(ctx, endpoint, acceptDNS, &response)
	if err != nil || status != http.StatusOK || response.Status == nil {
		return "unknown"
	}
	if len(response.Question) != 1 || !strings.EqualFold(strings.TrimSuffix(response.Question[0].Name, "."), domain) || response.Question[0].Type != dnsTypeNS {
		return "unknown"
	}
	switch *response.Status {
	case dnsRcodeNoError:
		return "NOERROR"
	case dnsRcodeNXDomain:
		return "NXDOMAIN"
	default:
		return fmt.Sprintf("RCODE_%d", *response.Status)
	}
}

func (checker *checker) check(ctx context.Context, entry *domainEntry) checkResult {
	result := checkResult{domainEntry: entry, Status: statusUnknown}
	result.RDAPURL = checker.rdapURL(entry.Domain)
	if result.RDAPURL == "" {
		result.Status = statusUnsupported
		result.Detail = "No HTTPS RDAP service in IANA bootstrap"
		return result
	}
	var response rdapResponse
	status, err := checker.getJSON(ctx, result.RDAPURL, acceptRDAP, &response)
	result.HTTPStatus = status
	// Some registries, including Verisign for .com and .net, answer 404 with an empty body.
	emptyNotFound := status == http.StatusNotFound && errors.Is(err, errEmptyBody)
	if err != nil && !emptyNotFound {
		result.Detail = err.Error()
		return result
	}
	if status == http.StatusNotFound {
		// A non-RDAP 404 body, such as a proxy error page, is not evidence of a missing domain.
		if !emptyNotFound && response.ErrorCode != http.StatusNotFound {
			result.Detail = "HTTP 404 with a body that is not an RDAP errorCode of 404"
			return result
		}
		for _, resolver := range checker.resolvers {
			result.DNS = append(result.DNS, checker.dnsStatus(ctx, resolver, entry.Domain))
		}
		if len(result.DNS) == 2 && result.DNS[0] == "NXDOMAIN" && result.DNS[1] == "NXDOMAIN" {
			result.Status = statusPossiblyUnregistered
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
	result.Status = statusRegistered
	result.RegistryStatus = response.Status
	for _, event := range response.Events {
		// Ignore "reregistration": some registries, such as Google Registry, use it for renewals.
		if event.Action != "registration" {
			continue
		}
		registered, err := time.Parse(time.RFC3339, event.Date)
		if err != nil {
			result.Status = statusUnknown
			result.Detail = "Invalid RDAP registration event date"
			return result
		}
		if result.Registered.IsZero() || registered.Before(result.Registered) {
			result.Registered = registered
		}
	}
	// Registry timestamps and commit clocks may differ; require a full day of separation.
	if !entry.FirstSeen.IsZero() && result.Registered.After(entry.FirstSeen.Add(24*time.Hour)) {
		result.Status = statusPossiblyReregistered
		result.Detail = "Registration postdates earliest rule history by more than 24 hours; ownership needs manual verification"
	}
	return result
}
