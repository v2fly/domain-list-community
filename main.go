package main

import (
	"bufio"
	"encoding/gob"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

type RuleType string

const (
	DOMAIN  RuleType = "domain"
	INCLUDE RuleType = "include"
	//REGEXP  RuleType = "regexp"
	//FULL    RuleType = "full"
	//RULE    RuleType = "rule"
)

type Entry struct {
	Type  string // full, domain, regexp, include
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

func removeCommentAttr(line string) string {
	idx := strings.Index(line, "#")
	if idx != -1 {
		line = strings.TrimSpace(line[:idx])
	}
	idx = strings.Index(line, "@")
	if idx != -1 {
		line = strings.TrimSpace(line[:idx])
	}
	return line
}

func readAllFromFile(dir string) (allRules map[string]*ParsedList, err error) {
	listRef := make(map[string]*List)
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		list, err := LoadFile(path)
		if err != nil {
			return err
		}
		listRef[list.Name] = list
		return nil
	})
	allRules = make(map[string]*ParsedList, len(listRef))
	for _, l := range listRef {
		parsedList, err := parseList(l, listRef)
		if err != nil {
			log.Println("err on parsing list", err)
			continue
		}
		allRules[l.Name] = parsedList
	}
	return
}

func LoadFile(path string) (list *List, err error) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	list = &List{Name: strings.ToUpper(filepath.Base(path))}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		line = removeCommentAttr(line)
		if len(line) == 0 {
			continue
		}
		entry, err := parseDomain(line)
		if err != nil {
			return list, err
		}
		list.Entry = append(list.Entry, *entry)
	}
	return
}

func parseDomain(domain string) (*Entry, error) {
	entry := Entry{}
	kv := strings.Split(domain, ":")
	if len(kv) == 1 { // prefix omitted
		entry.Type = string(DOMAIN)
		entry.Value = strings.ToLower(kv[0])
		return &entry, nil
	}

	if len(kv) == 2 {
		entry.Type = strings.ToLower(kv[0])
		entry.Value = strings.ToLower(kv[1])
		return &entry, nil
	}

	return nil, errors.New("Invalid format: " + domain)
}

func parseList(list *List, listRef map[string]*List) (*ParsedList, error) {
	pl := &ParsedList{
		Name:      list.Name,
		Inclusion: make(map[string]bool),
	}
	entryList := list.Entry
	hasInclude := true
	for hasInclude == true { // read inclusion recursively
		newEntryList := make([]Entry, 0, len(entryList))
		for _, entry := range entryList {
			if entry.Type == string(INCLUDE) {
				refName := strings.ToUpper(entry.Value)
				InclusionName := refName
				if pl.Inclusion[InclusionName] { // skip existed inclusion
					continue
				}
				pl.Inclusion[InclusionName] = true
				refList := listRef[refName]
				if refList == nil {
					return nil, errors.New(entry.Value + " not found.")
				}
				newEntryList = append(newEntryList, refList.Entry...)
			} else {
				newEntryList = append(newEntryList, entry)
				hasInclude = false
			}
		}
		entryList = newEntryList
	}
	pl.Entry = entryList

	return pl, nil
}

func WriteAllToGob(dir string, gobName string) (err error) {
	pl, err := readAllFromFile(dir)
	if err != nil {
		return
	}
	if _, err := os.Stat(gobName); err == nil {
		err = os.Remove(gobName)
		if err != nil {
			return err
		}
	}
	file, err := os.Create(gobName)
	if err != nil {
		return
	}
	err = gob.NewEncoder(file).Encode(pl)
	return
}

func main() {
	data := flag.String("data", "./data", "data dir path")
	outPath := flag.String("out", "rules.dat", "output path")
	flag.Parse()
	if WriteAllToGob(*data, *outPath) == nil {
		fmt.Printf("success build from %s to %s\n", *data, *outPath)
	}
}
