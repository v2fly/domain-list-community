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
	refMap    = make(map[string][]*Entry)
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

func writePlainList(exportedName string) error {
	targetList, exist := finalMap[strings.ToUpper(exportedName)]
	if !exist || len(targetList) == 0 {
		return fmt.Errorf("list %q does not exist or is empty.", exportedName)
	}
	file, err := os.Create(filepath.Join(*outputDir, strings.ToLower(exportedName)+".txt"))
	if err != nil {
		return err
	}
	defer file.Close()
	w := bufio.NewWriter(file)
	for _, entry := range targetList {
		fmt.Fprintln(w, entry.Plain)
	}
	return w.Flush()
}

func parseEntry(line string) (Entry, error) {
	var entry Entry
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return entry, fmt.Errorf("empty line: %q", line)
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

	// Parse/Check attributes and affiliations
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
	entry.Plain = entry.Type + ":" + entry.Value
	if len(entry.Attrs) != 0 {
		entry.Plain = entry.Plain + ":@" + strings.Join(entry.Attrs, ",@")
	}

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

func loadData(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	listName := strings.ToUpper(filepath.Base(path))
	if !validateSiteName(listName) {
		return fmt.Errorf("invalid list name: %s", listName)
	}
	scanner := bufio.NewScanner(file)
	lineIdx := 0
	for scanner.Scan() {
		line := scanner.Text()
		lineIdx++
		// Remove comments
		if idx := strings.Index(line, "#"); idx != -1 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		entry, err := parseEntry(line)
		if err != nil {
			return fmt.Errorf("error in %s at line %d: %v", path, lineIdx, err)
		}
		refMap[listName] = append(refMap[listName], &entry)
	}
	return nil
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
				return fmt.Errorf("affiliation is not allowed for include:%s", entry.Value)
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

func polishList(roughMap *map[string]*Entry) []*Entry {
	finalList := make([]*Entry, 0, len(*roughMap))
	queuingList := make([]*Entry, 0, len(*roughMap)) // Domain/full entries without attr
	domainsMap := make(map[string]bool)
	for _, entry := range *roughMap {
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
			if !strings.Contains(pd, ".") {
				break
			} // Not allow tld to be a parent
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
		return fmt.Errorf("circular inclusion in: %s", pl.Name)
	}
	cirIncMap[pl.Name] = true
	defer delete(cirIncMap, pl.Name)

	isMatchAttrFilters := func(entry *Entry, incFilter *Inclusion) bool {
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
	finalMap[pl.Name] = polishList(&roughMap)
	return nil
}

func main() {
	flag.Parse()

	dir := *dataPath
	fmt.Println("Use domain lists in", dir)

	// Generate refMap
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if err := loadData(path); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		fmt.Println("Failed to loadData:", err)
		os.Exit(1)
	}

	// Generate plMap
	for refName, refList := range refMap {
		if err := parseList(refName, refList); err != nil {
			fmt.Println("Failed to parseList:", err)
			os.Exit(1)
		}
	}

	// Generate finalMap
	for _, pl := range plMap {
		if err := resolveList(pl); err != nil {
			fmt.Println("Failed to resolveList:", err)
			os.Exit(1)
		}
	}

	// Create output directory if not exist
	if _, err := os.Stat(*outputDir); os.IsNotExist(err) {
		if mkErr := os.MkdirAll(*outputDir, 0755); mkErr != nil {
			fmt.Println("Failed to create output directory:", mkErr)
			os.Exit(1)
		}
	}

	// Export plaintext list
	var exportListSlice []string
	for raw := range strings.SplitSeq(*exportLists, ",") {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			exportListSlice = append(exportListSlice, trimmed)
		}
	}
	for _, exportList := range exportListSlice {
		if err := writePlainList(exportList); err != nil {
			fmt.Println("Failed to write list:", err)
			continue
		}
		fmt.Printf("list %q has been generated successfully.\n", exportList)
	}

	// Generate dat file
	protoList := new(router.GeoSiteList)
	for siteName, siteEntries := range finalMap {
		site, err := makeProtoList(siteName, siteEntries)
		if err != nil {
			fmt.Println("Failed to makeProtoList:", err)
			os.Exit(1)
		}
		protoList.Entry = append(protoList.Entry, site)
	}
	// Sort protoList so the marshaled list is reproducible
	slices.SortFunc(protoList.Entry, func(a, b *router.GeoSite) int {
		return strings.Compare(a.CountryCode, b.CountryCode)
	})

	protoBytes, err := proto.Marshal(protoList)
	if err != nil {
		fmt.Println("Failed to marshal:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(*outputDir, *outputName), protoBytes, 0644); err != nil {
		fmt.Println("Failed to write output:", err)
		os.Exit(1)
	} else {
		fmt.Println(*outputName, "has been generated successfully.")
	}
}
