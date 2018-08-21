package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/gogo/protobuf/proto"
	"v2ray.com/core/app/router"
)

type Entry struct {
	Type  string
	Value string
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
				Type:  router.Domain_Domain,
				Value: entry.Value,
			})
		case "regex":
			site.Domain = append(site.Domain, &router.Domain{
				Type:  router.Domain_Regex,
				Value: entry.Value,
			})
		case "keyword":
			site.Domain = append(site.Domain, &router.Domain{
				Type:  router.Domain_Plain,
				Value: entry.Value,
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

func parseEntry(line string) (Entry, error) {
	kv := strings.Split(line, ":")
	if len(kv) == 1 {
		return Entry{
			Type:  "domain",
			Value: kv[0],
		}, nil
	}
	if len(kv) == 2 {
		return Entry{
			Type:  strings.ToLower(kv[0]),
			Value: strings.ToLower(kv[1]),
		}, nil
	}
	return Entry{}, errors.New("Invalid format: " + line)
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
	for _, entry := range list.Entry {
		if entry.Type == "include" {
			if pl.Inclusion[entry.Value] {
				continue
			}
			pl.Inclusion[entry.Value] = true
			r := ref[entry.Value]
			if r == nil {
				return nil, errors.New(entry.Value + " not found.")
			}
			pl.Entry = append(pl.Entry, r.Entry...)
		} else {
			pl.Entry = append(pl.Entry, entry)
		}
	}

	return pl, nil
}

func main() {
	dir := filepath.Join(os.Getenv("GOPATH"), "src", "github.com", "v2ray", "domain-list-community", "data")
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
	}
}
