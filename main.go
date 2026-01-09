package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	router "github.com/v2fly/v2ray-core/v5/app/router/routercommon"
	"google.golang.org/protobuf/proto"
)

var (
	dataPath    = flag.String("datapath", "./data", "Path to your custom 'data' directory")
	outputName  = flag.String("outputname", "dlc.dat", "Name of the generated dat file")
	outputDir   = flag.String("outputdir", "./", "Directory to place all generated files")
	exportLists = flag.String("exportlists", "", "Lists to be flattened and exported in plaintext format, separated by ',' comma")
)

const (
	RuleTypeDomain     string = "domain"
	RuleTypeFullDomain string = "full"
	RuleTypeKeyword    string = "keyword"
	RuleTypeRegexp     string = "regexp"
	RuleTypeInclude    string = "include"
)

var (
	TypeChecker  = regexp.MustCompile(`^(domain|full|keyword|regexp|include)$`)
	ValueChecker = regexp.MustCompile(`^[a-z0-9!\.-]+$`)
	AttrChecker  = regexp.MustCompile(`^[a-z0-9!-]+$`)
)

var (
	refMap    = make(map[string]*List)
	plMap     = make(map[string]*ParsedList)
	finalMap  = make(map[string][]Entry)
	cirIncMap = make(map[string]bool) // Used for circular inclusion detection
)

type Entry struct {
	Type  string
	Value string
	Attrs []string
}

type Inclusion struct {
	Source    string
	MustAttrs []string
	BanAttrs  []string
}

type List struct {
	Name  string
	Entry []Entry
}

type ParsedList struct {
	Name       string
	Inclusions []Inclusion
	Entry      []Entry
}

func makeProtoList(listName string, entries *[]Entry) (*router.GeoSite, error) {
	site := &router.GeoSite{
		CountryCode: listName,
		Domain: make([]*router.Domain, 0, len(*entries)),
	}
	for _, entry := range *entries {
		pdomain := &router.Domain{Value: entry.Value}
		for _, attr := range entry.Attrs {
			pdomain.Attribute = append(pdomain.Attribute, &router.Domain_Attribute{
				Key:        attr,
				TypedValue: &router.Domain_Attribute_BoolValue{BoolValue: true},
			})
		}

		switch entry.Type {
		case RuleTypeDomain:
			pdomain.Type = router.Domain_RootDomain
		case RuleTypeRegexp:
			pdomain.Type = router.Domain_Regex
		case RuleTypeKeyword:
			pdomain.Type = router.Domain_Plain
		case RuleTypeFullDomain:
			pdomain.Type = router.Domain_Full
		}
		site.Domain = append(site.Domain, pdomain)
	}
	return site, nil
}

func writePlainList(exportedName string) error {
	targetList, exist := finalMap[strings.ToUpper(exportedName)]
	if !exist || len(targetList) == 0 {
		return fmt.Errorf("'%s' list does not exist or is empty.", exportedName)
	}
	file, err := os.Create(filepath.Join(*outputDir, strings.ToLower(exportedName) + ".txt"))
	if err != nil {
		return err
	}
	defer file.Close()
	w := bufio.NewWriter(file)
	for _, entry := range targetList {
		// Entry output format is: type:domain.tld:@attr1,@attr2
		var attrString string
		if entry.Attrs != nil {
			attrString = ":@" + strings.Join(entry.Attrs, ",@")
		}
		fmt.Fprintln(w, entry.Type + ":" + entry.Value + attrString)
	}
	return w.Flush()
}

func parseEntry(line string) (Entry, error) {
	var entry Entry
	parts := strings.Fields(line)

	// Parse type and value
	rawTypeVal := parts[0]
	kv := strings.Split(rawTypeVal, ":")
	if len(kv) == 1 {
		entry.Type = RuleTypeDomain // Default type
		entry.Value = strings.ToLower(rawTypeVal)
	} else if len(kv) == 2 {
		entry.Type = strings.ToLower(kv[0])
		if entry.Type == RuleTypeRegexp {
			entry.Value = kv[1]
		} else {
			entry.Value = strings.ToLower(kv[1])
		}
	} else {
		return entry, fmt.Errorf("invalid format: %s", line)
	}
	// Check type and value
	if !TypeChecker.MatchString(entry.Type) {
		return entry, fmt.Errorf("invalid type: %s", entry.Type)
	}
	if entry.Type == RuleTypeRegexp {
		if _, err := regexp.Compile(entry.Value); err != nil {
			return entry, fmt.Errorf("invalid regexp: %s", entry.Value)
		}
	} else if !ValueChecker.MatchString(entry.Value) {
		return entry, fmt.Errorf("invalid value: %s", entry.Value)
	}

	// Parse/Check attributes
	for _, part := range parts[1:] {
		if !strings.HasPrefix(part, "@") {
			return entry, fmt.Errorf("invalid attribute: %s", part)
		}
		attr := strings.ToLower(part[1:]) // Trim attribute prefix `@` character
		if !AttrChecker.MatchString(attr) {
			return entry, fmt.Errorf("invalid attribute key: %s", attr)
		}
		entry.Attrs = append(entry.Attrs, attr)
	}
	// Sort attributes
	sort.Slice(entry.Attrs, func(i, j int) bool {
		return entry.Attrs[i] < entry.Attrs[j]
	})

	return entry, nil
}

func Load(path string) (*List, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	list := &List{
		Name: strings.ToUpper(filepath.Base(path)),
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
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
			return nil, err
		}
		list.Entry = append(list.Entry, entry)
	}

	return list, nil
}

func parseList(refList *List) error {
	//TODO: one Entry -> multiple ParsedLists
	pl := &ParsedList{Name: refList.Name}
	for _, entry := range refList.Entry {
		if entry.Type == RuleTypeInclude {
			inc := Inclusion{Source: strings.ToUpper(entry.Value)}
			for _, attr := range entry.Attrs {
				if strings.HasPrefix(attr, "-") {
					inc.BanAttrs = append(inc.BanAttrs, attr[1:]) // Trim attribute prefix `-` character
				} else {
					inc.MustAttrs = append(inc.MustAttrs, attr)
				}
			}
			pl.Inclusions = append(pl.Inclusions, inc)
		} else {
			pl.Entry = append(pl.Entry, entry)
		}
	}
	plMap[refList.Name] = pl
	return nil
}

func resolveList(pl *ParsedList) error {
	if _, pldone := finalMap[pl.Name]; pldone { return nil }

	if cirIncMap[pl.Name] {
		return fmt.Errorf("circular inclusion in: %s", pl.Name)
	}
	cirIncMap[pl.Name] = true
	defer delete(cirIncMap, pl.Name)

	entry2String := func(e Entry) string { // Attributes already sorted
		return e.Type + ":" + e.Value + "@" + strings.Join(e.Attrs, "@")
	}
	isMatchAttrFilters := func(entry Entry, incFilter Inclusion) bool {
		if len(incFilter.MustAttrs) == 0 && len(incFilter.BanAttrs) == 0 { return true }
		if len(entry.Attrs) == 0 { return len(incFilter.MustAttrs) == 0 }

		attrMap := make(map[string]bool)
		for _, attr := range entry.Attrs {
			attrMap[attr] = true
		}
		for _, m := range incFilter.MustAttrs {
			if !attrMap[m] { return false }
		}
		for _, b := range incFilter.BanAttrs {
			if attrMap[b] { return false }
		}
		return true
	}

	bscDupMap := make(map[string]bool) // Used for basic duplicates detection
	var finalList []Entry
	for _, dentry := range pl.Entry {
		if dstring := entry2String(dentry); !bscDupMap[dstring] {
			bscDupMap[dstring] = true
			finalList = append(finalList, dentry)
		}
	}

	for _, inc := range pl.Inclusions {
		incPl, exist := plMap[inc.Source]
		if !exist {
			return fmt.Errorf("list '%s' includes a non-existent list: '%s'", pl.Name, inc.Source)
		}
		if err := resolveList(incPl); err != nil {
			return err
		}
		for _, ientry := range finalMap[inc.Source] {
			if isMatchAttrFilters(ientry, inc) {
				if istring := entry2String(ientry); !bscDupMap[istring] {
					bscDupMap[istring] = true
					finalList = append(finalList, ientry)
				}
			}
		}
	}
	finalMap[pl.Name] = finalList
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
		list, err := Load(path)
		if err != nil {
			return err
		}
		refMap[list.Name] = list
		return nil
	})
	if err != nil {
		fmt.Println("Failed:", err)
		os.Exit(1)
	}

	// Generate plMap
	for _, refList := range refMap {
		if err := parseList(refList); err != nil {
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
			fmt.Println("Failed:", mkErr)
			os.Exit(1)
		}
	}

	// Export plaintext list
	if *exportLists != "" {
		exportedListSlice := strings.Split(*exportLists, ",")
		for _, exportedList := range exportedListSlice {
			if err := writePlainList(exportedList); err != nil {
				fmt.Println("Failed to write list:", err)
				continue
			}
			fmt.Printf("list: '%s' has been generated successfully.\n", exportedList)
		}
	}

	// Generate dat file
	protoList := new(router.GeoSiteList)
	for siteName, siteEntries := range finalMap {
		site, err := makeProtoList(siteName, &siteEntries)
		if err != nil {
			fmt.Println("Failed:", err)
			os.Exit(1)
		}
		protoList.Entry = append(protoList.Entry, site)
	}
	// Sort protoList so the marshaled list is reproducible
	sort.SliceStable(protoList.Entry, func(i, j int) bool {
		return protoList.Entry[i].CountryCode < protoList.Entry[j].CountryCode
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
