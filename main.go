package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

type Entry struct {
	Type  string
	Value string
	Attrs []*router.Domain_Attribute
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

func (l *ParsedList) toPlainText(listName string) error {
	var entryBytes []byte
	for _, entry := range l.Entry {
		var attrString string
		if entry.Attrs != nil {
			for _, attr := range entry.Attrs {
				attrString += "@" + attr.GetKey() + ","
			}
			attrString = strings.TrimRight(":"+attrString, ",")
		}
		// Entry output format is: type:domain.tld:@attr1,@attr2
		entryBytes = append(entryBytes, []byte(entry.Type+":"+entry.Value+attrString+"\n")...)
	}
	if err := ioutil.WriteFile(filepath.Join(*outputDir, listName+".txt"), entryBytes, 0644); err != nil {
		return fmt.Errorf(err.Error())
	}
	return nil
}

func (l *ParsedList) toProto() (*router.GeoSite, error) {
	site := &router.GeoSite{
		CountryCode: l.Name,
	}
	for _, entry := range l.Entry {
		switch entry.Type {
		case "domain":
			site.Domain = append(site.Domain, &router.Domain{
				Type:      router.Domain_RootDomain,
				Value:     entry.Value,
				Attribute: entry.Attrs,
			})
		case "regexp":
			site.Domain = append(site.Domain, &router.Domain{
				Type:      router.Domain_Regex,
				Value:     entry.Value,
				Attribute: entry.Attrs,
			})
		case "keyword":
			site.Domain = append(site.Domain, &router.Domain{
				Type:      router.Domain_Plain,
				Value:     entry.Value,
				Attribute: entry.Attrs,
			})
		case "full":
			site.Domain = append(site.Domain, &router.Domain{
				Type:      router.Domain_Full,
				Value:     entry.Value,
				Attribute: entry.Attrs,
			})
		default:
			return nil, errors.New("unknown domain type: " + entry.Type)
		}
	}
	return site, nil
}

func exportPlainTextList(list []string, refName string, pl *ParsedList) {
	for _, listName := range list {
		if strings.EqualFold(refName, listName) {
			if err := pl.toPlainText(strings.ToLower(refName)); err != nil {
				fmt.Println("Failed: ", err)
				continue
			}
			fmt.Printf("'%s' has been generated successfully.\n", listName)
		}
	}
}

func removeComment(line string) string {
	idx := strings.Index(line, "#")
	if idx == -1 {
		return line
	}
	return strings.TrimSpace(line[:idx])
}

func parseDomain(domain string, entry *Entry) error {
	kv := strings.Split(domain, ":")
	if len(kv) == 1 {
		entry.Type = "domain"
		entry.Value = strings.ToLower(kv[0])
		return nil
	}

	if len(kv) == 2 {
		entry.Type = strings.ToLower(kv[0])
		entry.Value = strings.ToLower(kv[1])
		return nil
	}

	return errors.New("Invalid format: " + domain)
}

func parseAttribute(attr string) (*router.Domain_Attribute, error) {
	var attribute router.Domain_Attribute
	if len(attr) == 0 || attr[0] != '@' {
		return &attribute, errors.New("invalid attribute: " + attr)
	}

	// Trim attribute prefix `@` character
	attr = attr[1:]
	parts := strings.Split(attr, "=")
	if len(parts) == 1 {
		attribute.Key = strings.ToLower(parts[0])
		attribute.TypedValue = &router.Domain_Attribute_BoolValue{BoolValue: true}
	} else {
		attribute.Key = strings.ToLower(parts[0])
		intv, err := strconv.Atoi(parts[1])
		if err != nil {
			return &attribute, errors.New("invalid attribute: " + attr + ": " + err.Error())
		}
		attribute.TypedValue = &router.Domain_Attribute_IntValue{IntValue: int64(intv)}
	}
	return &attribute, nil
}

func parseEntry(line string) (Entry, error) {
	line = strings.TrimSpace(line)
	parts := strings.Split(line, " ")

	var entry Entry
	if len(parts) == 0 {
		return entry, errors.New("empty entry")
	}

	if err := parseDomain(parts[0], &entry); err != nil {
		return entry, err
	}

	for i := 1; i < len(parts); i++ {
		attr, err := parseAttribute(parts[i])
		if err != nil {
			return entry, err
		}
		entry.Attrs = append(entry.Attrs, attr)
	}

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
		line := strings.TrimSpace(scanner.Text())
		line = removeComment(line)
		if len(line) == 0 {
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

func isMatchAttr(Attrs []*router.Domain_Attribute, includeKey string) bool {
	isMatch := false
	mustMatch := true
	matchName := includeKey
	if strings.HasPrefix(includeKey, "!") {
		isMatch = true
		mustMatch = false
		matchName = strings.TrimLeft(includeKey, "!")
	}

	for _, Attr := range Attrs {
		attrName := Attr.Key
		if mustMatch {
			if matchName == attrName {
				isMatch = true
				break
			}
		} else {
			if matchName == attrName {
				isMatch = false
				break
			}
		}
	}
	return isMatch
}

func createIncludeAttrEntrys(list *List, matchAttr *router.Domain_Attribute) []Entry {
	newEntryList := make([]Entry, 0, len(list.Entry))
	matchName := matchAttr.Key
	for _, entry := range list.Entry {
		matched := isMatchAttr(entry.Attrs, matchName)
		if matched {
			newEntryList = append(newEntryList, entry)
		}
	}
	return newEntryList
}

func ParseList(list *List, ref map[string]*List) (*ParsedList, error) {
	pl := &ParsedList{
		Name:      list.Name,
		Inclusion: make(map[string]bool),
	}
	entryList := list.Entry
	for {
		newEntryList := make([]Entry, 0, len(entryList))
		hasInclude := false
		for _, entry := range entryList {
			if entry.Type == "include" {
				refName := strings.ToUpper(entry.Value)
				if entry.Attrs != nil {
					for _, attr := range entry.Attrs {
						InclusionName := strings.ToUpper(refName + "@" + attr.Key)
						if pl.Inclusion[InclusionName] {
							continue
						}
						pl.Inclusion[InclusionName] = true

						refList := ref[refName]
						if refList == nil {
							return nil, errors.New(entry.Value + " not found.")
						}
						attrEntrys := createIncludeAttrEntrys(refList, attr)
						if len(attrEntrys) != 0 {
							newEntryList = append(newEntryList, attrEntrys...)
						}
					}
				} else {
					InclusionName := refName
					if pl.Inclusion[InclusionName] {
						continue
					}
					pl.Inclusion[InclusionName] = true
					refList := ref[refName]
					if refList == nil {
						return nil, errors.New(entry.Value + " not found.")
					}
					newEntryList = append(newEntryList, refList.Entry...)
				}
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

	ref := make(map[string]*List)
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
		ref[list.Name] = list
		return nil
	})
	if err != nil {
		fmt.Println("Failed: ", err)
		os.Exit(1)
	}

	// Create output directory if not exist
	if _, err := os.Stat(*outputDir); os.IsNotExist(err) {
		if mkErr := os.MkdirAll(*outputDir, 0755); mkErr != nil {
			fmt.Println("Failed: ", mkErr)
			os.Exit(1)
		}
	}

	protoList := new(router.GeoSiteList)
	var existList []string
	for refName, list := range ref {
		pl, err := ParseList(list, ref)
		if err != nil {
			fmt.Println("Failed: ", err)
			os.Exit(1)
		}
		site, err := pl.toProto()
		if err != nil {
			fmt.Println("Failed: ", err)
			os.Exit(1)
		}
		protoList.Entry = append(protoList.Entry, site)

		// Flatten and export plaintext list
		if *exportLists != "" {
			if existList != nil {
				exportPlainTextList(existList, refName, pl)
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
					exportPlainTextList(existList, refName, pl)
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
	if err := ioutil.WriteFile(filepath.Join(*outputDir, *outputName), protoBytes, 0644); err != nil {
		fmt.Println("Failed: ", err)
		os.Exit(1)
	} else {
		fmt.Println(*outputName, "has been generated successfully.")
	}
}
