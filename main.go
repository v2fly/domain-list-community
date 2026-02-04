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

var (
	plMap     = make(map[string]*ParsedList)
	finalMap  = make(map[string][]*Entry)
	cirIncMap = make(map[string]bool) // Used for circular inclusion detection
)

type Entry struct {
	Type  string
	Value string
	Attrs []string
	Plain string
	Affs  []string
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

func parseEntry(line string) (Entry, error) {
	var entry Entry
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return entry, fmt.Errorf("empty line")
	}

	// Parse type and value
	v := parts[0]
	colonIndex := strings.Index(v, ":")
	if colonIndex == -1 {
		entry.Type = dlc.RuleTypeDomain // Default type
		entry.Value = strings.ToLower(v)
		if !validateDomainChars(entry.Value) {
			return entry, fmt.Errorf("invalid domain: %q", entry.Value)
		}
	} else {
		typ := strings.ToLower(v[:colonIndex])
		val := v[colonIndex+1:]
		switch typ {
		case dlc.RuleTypeRegexp:
			if _, err := regexp.Compile(val); err != nil {
				return entry, fmt.Errorf("invalid regexp %q: %w", val, err)
			}
			entry.Type = dlc.RuleTypeRegexp
			entry.Value = val
		case dlc.RuleTypeInclude:
			entry.Type = dlc.RuleTypeInclude
			entry.Value = strings.ToUpper(val)
			if !validateSiteName(entry.Value) {
				return entry, fmt.Errorf("invalid include list name: %q", entry.Value)
			}
		case dlc.RuleTypeDomain, dlc.RuleTypeFullDomain, dlc.RuleTypeKeyword:
			entry.Type = typ
			entry.Value = strings.ToLower(val)
			if !validateDomainChars(entry.Value) {
				return entry, fmt.Errorf("invalid domain: %q", entry.Value)
			}
		default:
			return entry, fmt.Errorf("invalid type: %q", typ)
		}
	}

	// Parse attributes and affiliations
	for _, part := range parts[1:] {
		if strings.HasPrefix(part, "@") {
			attr := strings.ToLower(part[1:]) // Trim attribute prefix `@` character
			if !validateAttrChars(attr) {
				return entry, fmt.Errorf("invalid attribute: %q", attr)
			}
			entry.Attrs = append(entry.Attrs, attr)
		} else if strings.HasPrefix(part, "&") {
			aff := strings.ToUpper(part[1:]) // Trim affiliation prefix `&` character
			if !validateSiteName(aff) {
				return entry, fmt.Errorf("invalid affiliation: %q", aff)
			}
			entry.Affs = append(entry.Affs, aff)
		} else {
			return entry, fmt.Errorf("invalid attribute/affiliation: %q", part)
		}
	}
	// Sort attributes
	slices.Sort(entry.Attrs)
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

	return entry, nil
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

func loadData(path string) ([]*Entry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var entries []*Entry
	scanner := bufio.NewScanner(file)
	lineIdx := 0
	for scanner.Scan() {
		line := scanner.Text()
		lineIdx++
		if idx := strings.Index(line, "#"); idx != -1 {
			line = line[:idx] // Remove comments
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		entry, err := parseEntry(line)
		if err != nil {
			return entries, fmt.Errorf("error in %q at line %d: %w", path, lineIdx, err)
		}
		entries = append(entries, &entry)
	}
	return entries, nil
}

func parseList(refName string, refList []*Entry) error {
	pl, _ := plMap[refName]
	if pl == nil {
		pl = &ParsedList{Name: refName}
		plMap[refName] = pl
	}
	for _, entry := range refList {
		if entry.Type == dlc.RuleTypeInclude {
			if len(entry.Affs) != 0 {
				return fmt.Errorf("affiliation is not allowed for include:%q", entry.Value)
			}
			inc := &Inclusion{Source: entry.Value}
			for _, attr := range entry.Attrs {
				if strings.HasPrefix(attr, "-") {
					inc.BanAttrs = append(inc.BanAttrs, attr[1:]) // Trim attribute prefix `-` character
				} else {
					inc.MustAttrs = append(inc.MustAttrs, attr)
				}
			}
			pl.Inclusions = append(pl.Inclusions, inc)
		} else {
			for _, aff := range entry.Affs {
				apl, _ := plMap[aff]
				if apl == nil {
					apl = &ParsedList{Name: aff}
					plMap[aff] = apl
				}
				apl.Entries = append(apl.Entries, entry)
			}
			pl.Entries = append(pl.Entries, entry)
		}
	}
	return nil
}

func isMatchAttrFilters(entry *Entry, incFilter *Inclusion) bool {
	if len(incFilter.MustAttrs) == 0 && len(incFilter.BanAttrs) == 0 {
		return true
	}
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
		case dlc.RuleTypeRegexp:
			finalList = append(finalList, entry)
		case dlc.RuleTypeKeyword:
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
			idx := strings.Index(pd, ".")
			if idx == -1 {
				break
			}
			pd = pd[idx+1:] // Go for next parent
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

func resolveList(pl *ParsedList) error {
	if _, pldone := finalMap[pl.Name]; pldone {
		return nil
	}

	if cirIncMap[pl.Name] {
		return fmt.Errorf("circular inclusion in: %q", pl.Name)
	}
	cirIncMap[pl.Name] = true
	defer delete(cirIncMap, pl.Name)

	roughMap := make(map[string]*Entry) // Avoid basic duplicates
	for _, dentry := range pl.Entries { // Add direct entries
		roughMap[dentry.Plain] = dentry
	}
	for _, inc := range pl.Inclusions {
		incPl, exist := plMap[inc.Source]
		if !exist {
			return fmt.Errorf("list %q includes a non-existent list: %q", pl.Name, inc.Source)
		}
		if err := resolveList(incPl); err != nil {
			return err
		}
		for _, ientry := range finalMap[inc.Source] {
			if isMatchAttrFilters(ientry, inc) { // Add included entries
				roughMap[ientry.Plain] = ientry
			}
		}
	}
	finalMap[pl.Name] = polishList(roughMap)
	return nil
}

func run() error {
	dir := *dataPath
	fmt.Printf("using domain lists data in %q\n", dir)

	// Generate refMap
	refMap := make(map[string][]*Entry)
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
		refMap[listName], err = loadData(path)
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to loadData: %w", err)
	}

	// Generate plMap
	for refName, refList := range refMap {
		if err := parseList(refName, refList); err != nil {
			return fmt.Errorf("failed to parseList %q: %w", refName, err)
		}
	}

	// Generate finalMap
	for plname, pl := range plMap {
		if err := resolveList(pl); err != nil {
			return fmt.Errorf("failed to resolveList %q: %w", plname, err)
		}
	}

	// Make sure output directory exists
	if err := os.MkdirAll(*outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Export plaintext list
	for rawEpList := range strings.SplitSeq(*exportLists, ",") {
		if epList := strings.TrimSpace(rawEpList); epList != "" {
			entries, exist := finalMap[strings.ToUpper(epList)]
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
	for siteName, siteEntries := range finalMap {
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
