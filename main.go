package main

import (
	"bufio"
	"encoding/json"
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
	datProfile  = flag.String("datprofile", "", "Path of config file used to assemble custom dats")
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

type GeoSites struct {
	Sites   []*router.GeoSite
	SiteIdx map[string]int
}

type DatTask struct {
	Name  string   `json:"name"`
	Mode  string   `json:"mode"`
	Lists []string `json:"lists"`
}

const (
	ModeAll       string = "all"
	ModeAllowlist string = "allowlist"
	ModeDenylist  string = "denylist"
)

func makeProtoList(listName string, entries []*Entry) *router.GeoSite {
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
	return site
}

func loadTasks(path string) ([]DatTask, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var tasks []DatTask
	dec := json.NewDecoder(f)
	if err := dec.Decode(&tasks); err != nil {
		return nil, fmt.Errorf("failed to decode json: %w", err)
	}
	for i, t := range tasks {
		if t.Name == "" {
			return nil, fmt.Errorf("task[%d]: name is required", i)
		}
		if !(t.Mode == ModeAll || t.Mode == ModeAllowlist || t.Mode == ModeDenylist) {
			return nil, fmt.Errorf("task[%d] %q: invalid mode %q", i, t.Name, t.Mode)
		}
	}
	return tasks, nil
}

func (gs *GeoSites) assembleDat(task DatTask) error {
	datFileName := strings.ToLower(filepath.Base(task.Name))
	geoSiteList := new(router.GeoSiteList)

	switch task.Mode {
	case ModeAll:
		geoSiteList.Entry = gs.Sites
	case ModeAllowlist:
		allowedIdxes := make([]int, 0, len(task.Lists))
		for _, list := range task.Lists {
			if idx, ok := gs.SiteIdx[strings.ToUpper(list)]; ok {
				allowedIdxes = append(allowedIdxes, idx)
			} else {
				return fmt.Errorf("list %q not found for allowlist task", list)
			}
		}
		slices.Sort(allowedIdxes)
		allowedlen := len(allowedIdxes)
		if allowedlen == 0 {
			return fmt.Errorf("allowlist needs at least one valid list")
		}
		geoSiteList.Entry = make([]*router.GeoSite, allowedlen)
		for i, idx := range allowedIdxes {
			geoSiteList.Entry[i] = gs.Sites[idx]
		}
	case ModeDenylist:
		deniedMap := make(map[int]bool, len(task.Lists))
		for _, list := range task.Lists {
			if idx, ok := gs.SiteIdx[strings.ToUpper(list)]; ok {
				deniedMap[idx] = true
			} else {
				fmt.Printf("[Warn] list %q not found in denylist task %q", list, task.Name)
			}
		}
		deniedlen := len(deniedMap)
		if deniedlen == 0 {
			fmt.Printf("[Warn] nothing to deny in task %q", task.Name)
			geoSiteList.Entry = gs.Sites
		} else {
			geoSiteList.Entry = make([]*router.GeoSite, 0, len(gs.Sites)-deniedlen)
			for i, site := range gs.Sites {
				if !deniedMap[i] {
					geoSiteList.Entry = append(geoSiteList.Entry, site)
				}
			}
		}
	}

	protoBytes, err := proto.Marshal(geoSiteList)
	if err != nil {
		return fmt.Errorf("failed to marshal: %w", err)
	}
	if err := os.WriteFile(filepath.Join(*outputDir, datFileName), protoBytes, 0644); err != nil {
		return fmt.Errorf("failed to write file %q: %w", datFileName, err)
	}
	fmt.Printf("dat %q has been generated successfully\n", datFileName)
	return nil
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

func parseEntry(typ, rule string) (*Entry, []string, error) {
	entry := &Entry{Type: typ}
	parts := strings.Fields(rule)
	if len(parts) == 0 {
		return entry, nil, fmt.Errorf("empty domain rule")
	}
	// Parse value
	switch entry.Type {
	case dlc.RuleTypeRegexp:
		if _, err := regexp.Compile(parts[0]); err != nil {
			return entry, nil, fmt.Errorf("invalid regexp %q: %w", parts[0], err)
		}
		entry.Value = parts[0]
	case dlc.RuleTypeDomain, dlc.RuleTypeFullDomain, dlc.RuleTypeKeyword:
		entry.Value = strings.ToLower(parts[0])
		if !validateDomainChars(entry.Value) {
			return entry, nil, fmt.Errorf("invalid domain: %q", entry.Value)
		}
	default:
		return entry, nil, fmt.Errorf("unknown rule type: %q", entry.Type)
	}
	plen := len(entry.Type) + len(entry.Value) + 1

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
			plen += 2 + len(attr)
		case '&':
			aff := strings.ToUpper(part[1:])
			if !validateSiteName(aff) {
				return entry, affs, fmt.Errorf("invalid affiliation: %q", aff)
			}
			affs = append(affs, aff)
		default:
			return entry, affs, fmt.Errorf("unknown field: %q", part)
		}
	}

	slices.Sort(entry.Attrs) // Sort attributes
	// Formated plain entry: type:domain.tld:@attr1,@attr2
	var plain strings.Builder
	plain.Grow(plen)
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
	return entry, affs, nil
}

func parseInclusion(rule string) (*Inclusion, error) {
	parts := strings.Fields(rule)
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty inclusion")
	}
	inc := &Inclusion{Source: strings.ToUpper(parts[0])}
	if !validateSiteName(inc.Source) {
		return inc, fmt.Errorf("invalid included list name: %q", inc.Source)
	}

	// Parse attributes
	for _, part := range parts[1:] {
		switch part[0] {
		case '@':
			attr := strings.ToLower(part[1:])
			if attr[0] == '-' {
				battr := attr[1:]
				if !validateAttrChars(battr) {
					return inc, fmt.Errorf("invalid ban attribute: %q", battr)
				}
				inc.BanAttrs = append(inc.BanAttrs, battr)
			} else {
				if !validateAttrChars(attr) {
					return inc, fmt.Errorf("invalid must attribute: %q", attr)
				}
				inc.MustAttrs = append(inc.MustAttrs, attr)
			}
		case '&':
			return inc, fmt.Errorf("affiliation is not allowed for inclusion")
		default:
			return inc, fmt.Errorf("unknown field: %q", part)
		}
	}
	return inc, nil
}

func validateDomainChars(domain string) bool {
	if domain == "" {
		return false
	}
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
	if attr == "" {
		return false
	}
	for i := range attr {
		c := attr[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '!' {
			continue
		}
		return false
	}
	return true
}

func validateSiteName(name string) bool {
	if name == "" {
		return false
	}
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
		typ, rule, isTypeSpecified := strings.Cut(line, ":")
		if !isTypeSpecified { // Default RuleType
			typ, rule = dlc.RuleTypeDomain, typ
		} else {
			typ = strings.ToLower(typ)
		}
		if typ == dlc.RuleTypeInclude {
			inc, err := parseInclusion(rule)
			if err != nil {
				return fmt.Errorf("error in %q at line %d: %w", path, lineIdx, err)
			}
			pl.Inclusions = append(pl.Inclusions, inc)
		} else {
			entry, affs, err := parseEntry(typ, rule)
			if err != nil {
				return fmt.Errorf("error in %q at line %d: %w", path, lineIdx, err)
			}
			for _, aff := range affs {
				apl := p.getOrCreateParsedList(aff)
				apl.Entries = append(apl.Entries, entry)
			}
			pl.Entries = append(pl.Entries, entry)
		}
	}
	return scanner.Err()
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
	if len(roughMap) == 0 {
		return fmt.Errorf("empty list")
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
	sitesCount := len(processor.plMap)
	processor.finalMap = make(map[string][]*Entry, sitesCount)
	processor.cirIncMap = make(map[string]bool)
	for plname := range processor.plMap {
		if err := processor.resolveList(plname); err != nil {
			return fmt.Errorf("failed to resolveList %q: %w", plname, err)
		}
	}
	processor.plMap = nil

	// Make sure output directory exists
	if err := os.MkdirAll(*outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}
	// Export plaintext lists
	for rawEpList := range strings.SplitSeq(*exportLists, ",") {
		if epList := strings.TrimSpace(rawEpList); epList != "" {
			entries, exist := processor.finalMap[strings.ToUpper(epList)]
			if !exist {
				fmt.Printf("[Warn] list %q does not exist\n", epList)
				continue
			}
			if err := writePlainList(epList, entries); err != nil {
				fmt.Printf("[Error] failed to write list %q: %v\n", epList, err)
				continue
			}
			fmt.Printf("list %q has been generated successfully\n", epList)
		}
	}

	// Generate proto sites
	gs := &GeoSites{
		Sites:   make([]*router.GeoSite, 0, sitesCount),
		SiteIdx: make(map[string]int, sitesCount),
	}
	for siteName, siteEntries := range processor.finalMap {
		gs.Sites = append(gs.Sites, makeProtoList(siteName, siteEntries))
	}
	processor = nil
	// Sort proto sites so the generated file is reproducible
	slices.SortFunc(gs.Sites, func(a, b *router.GeoSite) int {
		return strings.Compare(a.CountryCode, b.CountryCode)
	})
	for i := range sitesCount {
		gs.SiteIdx[gs.Sites[i].CountryCode] = i
	}

	// Load tasks and generate dat files
	var tasks []DatTask
	if *datProfile == "" {
		tasks = []DatTask{{Name: *outputName, Mode: ModeAll}}
	} else {
		var err error
		tasks, err = loadTasks(*datProfile)
		if err != nil {
			return fmt.Errorf("failed to loadTasks %q: %v", *datProfile, err)
		}
	}
	for _, task := range tasks {
		if err := gs.assembleDat(task); err != nil {
			fmt.Printf("[Error] failed to assembleDat %q: %v", task.Name, err)
		}
	}
	return nil
}

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Printf("[Fatal] critical error: %v\n", err)
		os.Exit(1)
	}
}
