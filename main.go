package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/v2fly/domain-list-community/internal/dlc"
	router "github.com/v2fly/v2ray-core/v5/app/router/routercommon"
	"google.golang.org/protobuf/proto"
)

var (
	dataPath    = flag.String("datapath", "./data", "Path to your custom 'data' directory")
	outputName  = flag.String("outputname", "dlc.dat", "Name of the generated dat file")
	outputDir   = flag.String("outputdir", "./", "Directory to place all generated files")
	exportLists = flag.String("exportlists", "", "Lists to be flattened and exported in plaintext format, separated by ',' comma")
)

type Entry struct {
	Type  string
	Value string
	Attrs []string
	Plain string
}

type Inclusion struct {
	Source    string
	MustAttrs []string
	BanAttrs  []string
}

type ParsedList struct {
	Name       string
	Inclusions []*Inclusion
	Entries    []*Entry
}

type Processor struct {
	plMap     map[string]*ParsedList
	finalMap  map[string][]*Entry
	cirIncMap map[string]bool
}

func makeProtoList(listName string, entries []*Entry) (*router.GeoSite, error) {
	site := &router.GeoSite{
		CountryCode: listName,
		Domain:      make([]*router.Domain, 0, len(entries)),
	}
	for _, entry := range entries {
		pdomain := &router.Domain{Value: entry.Value}
		for _, attr := range entry.Attrs {
			pdomain.Attribute = append(pdomain.Attribute, &router.Domain_Attribute{
				Key:        attr,
				TypedValue: &router.Domain_Attribute_BoolValue{BoolValue: true},
			})
		}

		switch entry.Type {
		case dlc.RuleTypeDomain:
			pdomain.Type = router.Domain_RootDomain
		case dlc.RuleTypeRegexp:
			pdomain.Type = router.Domain_Regex
		case dlc.RuleTypeKeyword:
			pdomain.Type = router.Domain_Plain
		case dlc.RuleTypeFullDomain:
			pdomain.Type = router.Domain_Full
		}
		site.Domain = append(site.Domain, pdomain)
	}
	return site, nil
}

func writePlainList(listname string, entries []*Entry) error {
	file, err := os.Create(filepath.Join(*outputDir, strings.ToLower(listname)+".txt"))
	if err != nil {
		return err
	}
	defer file.Close()
	w := bufio.NewWriter(file)
	for _, entry := range entries {
		fmt.Fprintln(w, entry.Plain)
	}
	return w.Flush()
}

func parseEntry(line string) (*Entry, []string, error) {
	entry := new(Entry)
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return entry, nil, fmt.Errorf("empty line")
	}

	// Parse type and value
	typ, val, isTypeSpecified := strings.Cut(parts[0], ":")
	typ = strings.ToLower(typ)
	if !isTypeSpecified { // Default RuleType
		if !validateDomainChars(typ) {
			return entry, nil, fmt.Errorf("invalid domain: %q", typ)
		}
		entry.Type = dlc.RuleTypeDomain
		entry.Value = typ
	} else {
		switch typ {
		case dlc.RuleTypeRegexp:
			if _, err := regexp.Compile(val); err != nil {
				return entry, nil, fmt.Errorf("invalid regexp %q: %w", val, err)
			}
			entry.Type = dlc.RuleTypeRegexp
			entry.Value = val
		case dlc.RuleTypeInclude:
			entry.Type = dlc.RuleTypeInclude
			entry.Value = strings.ToUpper(val)
			if !validateSiteName(entry.Value) {
				return entry, nil, fmt.Errorf("invalid included list name: %q", entry.Value)
			}
		case dlc.RuleTypeDomain, dlc.RuleTypeFullDomain, dlc.RuleTypeKeyword:
			entry.Type = typ
			entry.Value = strings.ToLower(val)
			if !validateDomainChars(entry.Value) {
				return entry, nil, fmt.Errorf("invalid domain: %q", entry.Value)
			}
		default:
			return entry, nil, fmt.Errorf("invalid type: %q", typ)
		}
	}

	// Parse attributes and affiliations
	var affs []string
	for _, part := range parts[1:] {
		switch part[0] {
		case '@':
			attr := strings.ToLower(part[1:])
			if !validateAttrChars(attr) {
				return entry, affs, fmt.Errorf("invalid attribute: %q", attr)
			}
			entry.Attrs = append(entry.Attrs, attr)
		case '&':
			aff := strings.ToUpper(part[1:])
			if !validateSiteName(aff) {
				return entry, affs, fmt.Errorf("invalid affiliation: %q", aff)
			}
			affs = append(affs, aff)
		default:
			return entry, affs, fmt.Errorf("invalid attribute/affiliation: %q", part)
		}
	}

	if entry.Type != dlc.RuleTypeInclude {
		slices.Sort(entry.Attrs) // Sort attributes
		// Formated plain entry: type:domain.tld:@attr1,@attr2
		var plain strings.Builder
		plain.Grow(len(entry.Type) + len(entry.Value) + 10)
		plain.WriteString(entry.Type)
		plain.WriteByte(':')
		plain.WriteString(entry.Value)
		for i, attr := range entry.Attrs {
			if i == 0 {
				plain.WriteByte(':')
			} else {
				plain.WriteByte(',')
			}
			plain.WriteByte('@')
			plain.WriteString(attr)
		}
		entry.Plain = plain.String()
	}
	return entry, affs, nil
}

func validateDomainChars(domain string) bool {
	for i := range domain {
		c := domain[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func validateAttrChars(attr string) bool {
	for i := range attr {
		c := attr[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '!' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func validateSiteName(name string) bool {
	for i := range name {
		c := name[i]
		if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '!' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func (p *Processor) getOrCreateParsedList(name string) *ParsedList {
	pl, exist := p.plMap[name]
	if !exist {
		pl = &ParsedList{Name: name}
		p.plMap[name] = pl
	}
	return pl
}

func (p *Processor) loadData(listName string, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	pl := p.getOrCreateParsedList(listName)
	scanner := bufio.NewScanner(file)
	lineIdx := 0
	for scanner.Scan() {
		lineIdx++
		line, _, _ := strings.Cut(scanner.Text(), "#") // Remove comments
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		entry, affs, err := parseEntry(line)
		if err != nil {
			return fmt.Errorf("error in %q at line %d: %w", path, lineIdx, err)
		}

		if entry.Type == dlc.RuleTypeInclude {
			inc := &Inclusion{Source: entry.Value}
			for _, attr := range entry.Attrs {
				if attr[0] == '-' {
					inc.BanAttrs = append(inc.BanAttrs, attr[1:])
				} else {
					inc.MustAttrs = append(inc.MustAttrs, attr)
				}
			}
			for _, aff := range affs {
				apl := p.getOrCreateParsedList(aff)
				apl.Inclusions = append(apl.Inclusions, inc)
			}
			pl.Inclusions = append(pl.Inclusions, inc)
		} else {
			for _, aff := range affs {
				apl := p.getOrCreateParsedList(aff)
				apl.Entries = append(apl.Entries, entry)
			}
			pl.Entries = append(pl.Entries, entry)
		}
	}
	return nil
}

func isMatchAttrFilters(entry *Entry, incFilter *Inclusion) bool {
	if len(entry.Attrs) == 0 {
		return len(incFilter.MustAttrs) == 0
	}
	for _, m := range incFilter.MustAttrs {
		if !slices.Contains(entry.Attrs, m) {
			return false
		}
	}
	for _, b := range incFilter.BanAttrs {
		if slices.Contains(entry.Attrs, b) {
			return false
		}
	}
	return true
}

func polishList(roughMap map[string]*Entry) []*Entry {
	finalList := make([]*Entry, 0, len(roughMap))
	queuingList := make([]*Entry, 0, len(roughMap)) // Domain/full entries without attr
	domainsMap := make(map[string]bool)
	for _, entry := range roughMap {
		switch entry.Type { // Bypass regexp, keyword and "full/domain with attr"
		case dlc.RuleTypeRegexp, dlc.RuleTypeKeyword:
			finalList = append(finalList, entry)
		case dlc.RuleTypeDomain:
			domainsMap[entry.Value] = true
			if len(entry.Attrs) != 0 {
				finalList = append(finalList, entry)
			} else {
				queuingList = append(queuingList, entry)
			}
		case dlc.RuleTypeFullDomain:
			if len(entry.Attrs) != 0 {
				finalList = append(finalList, entry)
			} else {
				queuingList = append(queuingList, entry)
			}
		}
	}
	// Remove redundant subdomains for full/domain without attr
	for _, qentry := range queuingList {
		isRedundant := false
		pd := qentry.Value // To be parent domain
		if qentry.Type == dlc.RuleTypeFullDomain {
			pd = "." + pd // So that `domain:example.org` overrides `full:example.org`
		}
		for {
			var hasParent bool
			_, pd, hasParent = strings.Cut(pd, ".") // Go for next parent
			if !hasParent {
				break
			}
			if domainsMap[pd] {
				isRedundant = true
				break
			}
		}
		if !isRedundant {
			finalList = append(finalList, qentry)
		}
	}
	// Sort final entries
	slices.SortFunc(finalList, func(a, b *Entry) int {
		return strings.Compare(a.Plain, b.Plain)
	})
	return finalList
}

func (p *Processor) resolveList(plname string) error {
	if _, pldone := p.finalMap[plname]; pldone {
		return nil
	}
	pl, plexist := p.plMap[plname]
	if !plexist {
		return fmt.Errorf("list %q not found", plname)
	}
	if p.cirIncMap[plname] {
		return fmt.Errorf("circular inclusion in: %q", plname)
	}
	p.cirIncMap[plname] = true
	defer delete(p.cirIncMap, plname)

	roughMap := make(map[string]*Entry) // Avoid basic duplicates
	for _, dentry := range pl.Entries { // Add direct entries
		roughMap[dentry.Plain] = dentry
	}
	for _, inc := range pl.Inclusions { // Add included entries
		if err := p.resolveList(inc.Source); err != nil {
			return fmt.Errorf("failed to resolve inclusion %q: %w", inc.Source, err)
		}
		isFullInc := len(inc.MustAttrs) == 0 && len(inc.BanAttrs) == 0
		for _, ientry := range p.finalMap[inc.Source] {
			if isFullInc || isMatchAttrFilters(ientry, inc) {
				roughMap[ientry.Plain] = ientry
			}
		}
	}
	p.finalMap[plname] = polishList(roughMap)
	return nil
}

func run() error {
	dir := *dataPath
	fmt.Printf("using domain lists data in %q\n", dir)

	// Generate plMap
	processor := &Processor{plMap: make(map[string]*ParsedList)}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		listName := strings.ToUpper(filepath.Base(path))
		if !validateSiteName(listName) {
			return fmt.Errorf("invalid list name: %q", listName)
		}
		return processor.loadData(listName, path)
	})
	if err != nil {
		return fmt.Errorf("failed to loadData: %w", err)
	}
	// Generate finalMap
	processor.finalMap = make(map[string][]*Entry, len(processor.plMap))
	processor.cirIncMap = make(map[string]bool)
	for plname := range processor.plMap {
		if err := processor.resolveList(plname); err != nil {
			return fmt.Errorf("failed to resolveList %q: %w", plname, err)
		}
	}

	// Make sure output directory exists
	if err := os.MkdirAll(*outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}
	// Export plaintext lists
	for rawEpList := range strings.SplitSeq(*exportLists, ",") {
		if epList := strings.TrimSpace(rawEpList); epList != "" {
			entries, exist := processor.finalMap[strings.ToUpper(epList)]
			if !exist || len(entries) == 0 {
				fmt.Printf("list %q does not exist or is empty\n", epList)
				continue
			}
			if err := writePlainList(epList, entries); err != nil {
				fmt.Printf("failed to write list %q: %v\n", epList, err)
				continue
			}
			fmt.Printf("list %q has been generated successfully.\n", epList)
		}
	}

	// Generate dat file
	protoList := new(router.GeoSiteList)
	for siteName, siteEntries := range processor.finalMap {
		site, err := makeProtoList(siteName, siteEntries)
		if err != nil {
			return fmt.Errorf("failed to makeProtoList %q: %w", siteName, err)
		}
		protoList.Entry = append(protoList.Entry, site)
	}
	// Sort protoList so the marshaled list is reproducible
	slices.SortFunc(protoList.Entry, func(a, b *router.GeoSite) int {
		return strings.Compare(a.CountryCode, b.CountryCode)
	})

	protoBytes, err := proto.Marshal(protoList)
	if err != nil {
		return fmt.Errorf("failed to marshal: %w", err)
	}
	if err := os.WriteFile(filepath.Join(*outputDir, *outputName), protoBytes, 0644); err != nil {
		return fmt.Errorf("failed to write output: %w", err)
	}
	fmt.Printf("%q has been generated successfully.\n", *outputName)
	return nil
}

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Printf("Fatal error: %v\n", err)
		os.Exit(1)
	}
}
