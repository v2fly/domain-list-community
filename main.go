package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"go/build"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/golang/protobuf/proto"
	"v2ray.com/core/app/router"
)

var (
	dataPath        = flag.String("datapath", "", "Path to your custom 'data' directory")
	defaultDataPath = filepath.Join("src", "github.com", "v2fly", "domain-list-community", "data")
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

func (l *ParsedList) toProto() (*router.GeoSite, error) {
	site := &router.GeoSite{
		CountryCode: l.Name,
	}
	for _, entry := range l.Entry {
		switch entry.Type {
		case "domain":
			site.Domain = append(site.Domain, &router.Domain{
				Type:      router.Domain_Domain,
				Value:     entry.Value,
				Attribute: entry.Attrs,
			})
		case "regex":
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

func parseAttribute(attr string) (router.Domain_Attribute, error) {
	var attribute router.Domain_Attribute
	if len(attr) == 0 || attr[0] != '@' {
		return attribute, errors.New("invalid attribute: " + attr)
	}

	attr = attr[0:]
	parts := strings.Split(attr, "=")
	if len(parts) == 1 {
		attribute.Key = strings.ToLower(parts[0])
		attribute.TypedValue = &router.Domain_Attribute_BoolValue{BoolValue: true}
	} else {
		attribute.Key = strings.ToLower(parts[0])
		intv, err := strconv.Atoi(parts[1])
		if err != nil {
			return attribute, errors.New("invalid attribute: " + attr + ": " + err.Error())
		}
		attribute.TypedValue = &router.Domain_Attribute_IntValue{IntValue: int64(intv)}
	}
	return attribute, nil
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
		entry.Attrs = append(entry.Attrs, &attr)
	}

	return entry, nil
}

func DetectPath(path string) (string, error) {
	arrPath := strings.Split(path, string(filepath.ListSeparator))
	for _, content := range arrPath {
		fullPath := filepath.Join(content, defaultDataPath)
		_, err := os.Stat(fullPath)
		if err == nil || os.IsExist(err) {
			return fullPath, nil
		}
	}
	err := fmt.Errorf("directory '%s' not found in '$GOPATH'", defaultDataPath)
	return "", err
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
				if pl.Inclusion[entry.Value] {
					continue
				}
				refName := strings.ToUpper(entry.Value)
				pl.Inclusion[refName] = true
				r := ref[refName]
				if r == nil {
					return nil, errors.New(entry.Value + " not found.")
				}
				newEntryList = append(newEntryList, r.Entry...)
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

func envFile() (string, error) {
	if file := os.Getenv("GOENV"); file != "" {
		if file == "off" {
			return "", fmt.Errorf("GOENV=off")
		}
		return file, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	if dir == "" {
		return "", fmt.Errorf("missing user-config dir")
	}
	return filepath.Join(dir, "go", "env"), nil
}

func getRuntimeEnv(key string) (string, error) {
	file, err := envFile()
	if err != nil {
		return "", err
	}
	if file == "" {
		return "", fmt.Errorf("missing runtime env file")
	}
	var data []byte
	var runtimeEnv string
	data, err = ioutil.ReadFile(file)
	envStrings := strings.Split(string(data), "\n")
	for _, envItem := range envStrings {
		envItem = strings.TrimSuffix(envItem, "\r")
		envKeyValue := strings.Split(envItem, "=")
		if strings.ToLower(envKeyValue[0]) == strings.ToLower(key) {
			runtimeEnv = envKeyValue[1]
		}
	}
	return runtimeEnv, nil
}

func main() {
	flag.Parse()

	var dir string
	var err error
	if *dataPath != "" {
		dir = *dataPath
	} else {
		goPath, envErr := getRuntimeEnv("GOPATH")
		if envErr != nil {
			fmt.Println("Failed: please set '$GOPATH' manually, or use 'datapath' option to specify the path to your custom 'data' directory")
			return
		}
		if goPath == "" {
			goPath = build.Default.GOPATH
		}
		fmt.Println("Use $GOPATH:", goPath)
		fmt.Printf("Searching directory '%s' in '%s'...\n", defaultDataPath, goPath)
		dir, err = DetectPath(goPath)
	}
	if err != nil {
		fmt.Println("Failed: ", err)
		return
	}
	fmt.Println("Use domain lists in", dir)

	ref := make(map[string]*List)
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
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
		return
	}
	protoList := new(router.GeoSiteList)
	for _, list := range ref {
		pl, err := ParseList(list, ref)
		if err != nil {
			fmt.Println("Failed: ", err)
			return
		}
		site, err := pl.toProto()
		if err != nil {
			fmt.Println("Failed: ", err)
			return
		}
		protoList.Entry = append(protoList.Entry, site)
	}

	protoBytes, err := proto.Marshal(protoList)
	if err != nil {
		fmt.Println("Failed:", err)
		return
	}
	if err := ioutil.WriteFile("dlc.dat", protoBytes, 0777); err != nil {
		fmt.Println("Failed: ", err)
	} else {
		fmt.Println("dlc.dat has been generated successfully in the directory. You can rename 'dlc.dat' to 'geosite.dat' and use it in V2Ray.")
	}
}
