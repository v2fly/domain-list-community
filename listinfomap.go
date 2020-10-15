package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"v2ray.com/core/app/router"
)

// ListInfoMap is the map of files in data directory and ListInfo
type ListInfoMap map[fileName]*ListInfo

// Marshal processes a file in data directory and generates ListInfo for it.
func (lm *ListInfoMap) Marshal(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	list := NewListInfo()
	listName := fileName(strings.ToUpper(filepath.Base(path)))
	list.Name = listName
	if err := list.ProcessList(file); err != nil {
		return err
	}

	(*lm)[listName] = list
	return nil
}

// FlattenAndGenUniqueDomainList flattens the included lists and
// generates a domain trie for each file in data directory to
// make the items of domain type list unique.
func (lm *ListInfoMap) FlattenAndGenUniqueDomainList() error {
	inclusionLevel := make([]map[fileName]bool, 0, 20)
	okayList := make(map[fileName]bool)
	inclusionLevelAllLength, loopTimes := 0, 0

	for inclusionLevelAllLength < len(*lm) {
		inclusionMap := make(map[fileName]bool)

		if loopTimes == 0 {
			for _, listinfo := range *lm {
				if listinfo.HasInclusion {
					continue
				}
				inclusionMap[listinfo.Name] = true
			}
		} else {
			for _, listinfo := range *lm {
				if !listinfo.HasInclusion || okayList[listinfo.Name] {
					continue
				}

				var passTimes int
				for filename := range listinfo.InclusionAttributeMap {
					if !okayList[filename] {
						break
					}
					passTimes++
				}
				if passTimes == len(listinfo.InclusionAttributeMap) {
					inclusionMap[listinfo.Name] = true
				}
			}
		}

		for filename := range inclusionMap {
			okayList[filename] = true
		}

		inclusionLevel = append(inclusionLevel, inclusionMap)
		inclusionLevelAllLength += len(inclusionMap)
		loopTimes++
	}

	for _, inclusionMap := range inclusionLevel {
		for inclusionFilename := range inclusionMap {
			if err := (*lm)[inclusionFilename].Flatten(lm); err != nil {
				return err
			}
		}
	}

	return nil
}

// ToProto generates a router.GeoSite for each file in data directory
// and returns a router.GeoSiteList
func (lm *ListInfoMap) ToProto(excludeAttrs map[fileName]map[attribute]bool) *router.GeoSiteList {
	protoList := new(router.GeoSiteList)
	for _, listinfo := range *lm {
		listinfo.ToGeoSite(excludeAttrs)
		protoList.Entry = append(protoList.Entry, listinfo.GeoSite)
	}
	return protoList
}

// ToPlainText returns a map of exported lists that user wants
// and the contents of them in byte format.
func (lm *ListInfoMap) ToPlainText(exportListsMap []string) (map[string][]byte, error) {
	filePlainTextBytesMap := make(map[string][]byte)
	for _, filename := range exportListsMap {
		if listinfo := (*lm)[fileName(strings.ToUpper(filename))]; listinfo != nil {
			plaintextBytes := listinfo.ToPlainText()
			filePlainTextBytesMap[filename] = plaintextBytes
		} else {
			fmt.Println("Notice: " + filename + ": no such exported list in the directory, skipped.")
		}
	}
	return filePlainTextBytesMap, nil
}

// ToGFWList returns the content of the list to be generated into GFWList format
// that user wants in bytes format.
func (lm *ListInfoMap) ToGFWList(togfwlist string) ([]byte, error) {
	if togfwlist != "" {
		if listinfo := (*lm)[fileName(strings.ToUpper(togfwlist))]; listinfo != nil {
			return listinfo.ToGFWList(), nil
		}
		return nil, errors.New("no such list: " + togfwlist)
	}
	return nil, nil
}
