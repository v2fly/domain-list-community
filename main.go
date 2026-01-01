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
	refMap = make(map[string]*List)
)

type Entry struct {
	Type  string
	Value string
	Attrs []string
}

type List struct {
	Name  string
	Entry []Entry
}

type ParsedList struct {
	Name      string
	Inclusion map[string]bool
	Entry     []Entry
}

func (l *ParsedList) toPlainText() error {
	var entryBytes []byte
	for _, entry := range l.Entry {
		var attrString string
		if entry.Attrs != nil {
			attrString = ":@" + strings.Join(entry.Attrs, ",@")
		}
		// Entry output format is: type:domain.tld:@attr1,@attr2
		entryBytes = append(entryBytes, []byte(entry.Type + ":" + entry.Value + attrString + "\n")...)
	}
	if err := os.WriteFile(filepath.Join(*outputDir, strings.ToLower(l.Name) + ".txt"), entryBytes, 0644); err != nil {
		return err
	}
	return nil
}

func (l *ParsedList) toProto() (*router.GeoSite, error) {
	site := &router.GeoSite{
		CountryCode: l.Name,
	}
	for _, entry := range l.Entry {
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

func exportPlainTextList(exportFiles []string, entryList *ParsedList) {
	for _, exportfilename := range exportFiles {
		if strings.EqualFold(entryList.Name, exportfilename) {
			if err := entryList.toPlainText(); err != nil {
				fmt.Println("Failed to exportPlainTextList:", err)
				continue
			}
			fmt.Printf("'%s' has been generated successfully.\n", exportfilename)
		}
	}
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

func ParseList(refList *List) (*ParsedList, error) {
	pl := &ParsedList{
		Name:      refList.Name,
		Inclusion: make(map[string]bool),
	}
	entryList := refList.Entry
	for {
		newEntryList := make([]Entry, 0, len(entryList))
		hasInclude := false
		for _, entry := range entryList {
			if entry.Type == RuleTypeInclude {
				refName := strings.ToUpper(entry.Value)
				if pl.Inclusion[refName] {
					continue
				}
				pl.Inclusion[refName] = true
				refList := refMap[refName]
				if refList == nil {
					return nil, fmt.Errorf("list not found: %s", entry.Value)
				}
				newEntryList = append(newEntryList, refList.Entry...)
				hasInclude = true
			} else {
				newEntryList = append(newEntryList, entry)
			}
		}
		entryList = newEntryList
		if !hasInclude {
			break
		}
	}
	pl.Entry = entryList

	return pl, nil
}

func main() {
	flag.Parse()

	dir := *dataPath
	fmt.Println("Use domain lists in", dir)

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

	// Create output directory if not exist
	if _, err := os.Stat(*outputDir); os.IsNotExist(err) {
		if mkErr := os.MkdirAll(*outputDir, 0755); mkErr != nil {
			fmt.Println("Failed:", mkErr)
			os.Exit(1)
		}
	}

	protoList := new(router.GeoSiteList)
	var existList []string
	for _, refList := range refMap {
		pl, err := ParseList(refList)
		if err != nil {
			fmt.Println("Failed:", err)
			os.Exit(1)
		}
		site, err := pl.toProto()
		if err != nil {
			fmt.Println("Failed:", err)
			os.Exit(1)
		}
		protoList.Entry = append(protoList.Entry, site)

		// Flatten and export plaintext list
		if *exportLists != "" {
			if existList != nil {
				exportPlainTextList(existList, pl)
			} else {
				exportedListSlice := strings.Split(*exportLists, ",")
				for _, exportedListName := range exportedListSlice {
					fileName := filepath.Join(dir, exportedListName)
					_, err := os.Stat(fileName)
					if err == nil || os.IsExist(err) {
						existList = append(existList, exportedListName)
					} else {
						fmt.Printf("'%s' list does not exist in '%s' directory.\n", exportedListName, dir)
					}
				}
				if existList != nil {
					exportPlainTextList(existList, pl)
				}
			}
		}
	}

	// Sort protoList so the marshaled list is reproducible
	sort.SliceStable(protoList.Entry, func(i, j int) bool {
		return protoList.Entry[i].CountryCode < protoList.Entry[j].CountryCode
	})

	protoBytes, err := proto.Marshal(protoList)
	if err != nil {
		fmt.Println("Failed:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(*outputDir, *outputName), protoBytes, 0644); err != nil {
		fmt.Println("Failed:", err)
		os.Exit(1)
	} else {
		fmt.Println(*outputName, "has been generated successfully.")
	}
}
