package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/proto"
)

var (
	dataPath     = flag.String("datapath", filepath.Join("./", "data"), "Path to your custom 'data' directory")
	outputPath   = flag.String("outputpath", "./", "Output path to the generated files")
	exportLists  = flag.String("exportlists", "", "Lists to be exported in plaintext format, separated by ',' comma")
	excludeAttrs = flag.String("excludeattrs", "", "Exclude rules with certain attributes in certain lists, seperated by ',' comma, support multiple attributes in one list. Example: geolocation-!cn@cn@ads,geolocation-cn@!cn")
	toGFWList    = flag.String("togfwlist", "geolocation-!cn", "List to be exported in GFWList format")
)

func main() {
	flag.Parse()

	dir := GetDataDir()
	listInfoMap := make(ListInfoMap)

	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if err := listInfoMap.Marshal(path); err != nil {
			return err
		}
		return nil
	}); err != nil {
		fmt.Println("Failed:", err)
		os.Exit(1)
	}

	if err := listInfoMap.FlattenAndGenUniqueDomainList(); err != nil {
		fmt.Println("Failed:", err)
		os.Exit(1)
	}

	// Process and split *excludeRules
	excludeAttrsInFile := make(map[fileName]map[attribute]bool)
	if *excludeAttrs != "" {
		exFilenameAttrSlice := strings.Split(*excludeAttrs, ",")
		for _, exFilenameAttr := range exFilenameAttrSlice {
			exFilenameAttr = strings.TrimSpace(exFilenameAttr)
			exFilenameAttrMap := strings.Split(exFilenameAttr, "@")
			filename := fileName(strings.ToUpper(strings.TrimSpace(exFilenameAttrMap[0])))
			excludeAttrsInFile[filename] = make(map[attribute]bool)
			for _, attr := range exFilenameAttrMap[1:] {
				attr = strings.TrimSpace(attr)
				if len(attr) > 0 {
					excludeAttrsInFile[filename][attribute(attr)] = true
				}
			}
		}
	}

	// Process and split *exportLists
	var exportListsSlice []string
	if *exportLists != "" {
		tempSlice := strings.Split(*exportLists, ",")
		for _, exportList := range tempSlice {
			exportList = strings.TrimSpace(exportList)
			if len(exportList) > 0 {
				exportListsSlice = append(exportListsSlice, exportList)
			}
		}
	}

	// Generate dlc.dat
	if geositeList := listInfoMap.ToProto(excludeAttrsInFile); geositeList != nil {
		protoBytes, err := proto.Marshal(geositeList)
		if err != nil {
			fmt.Println("Failed:", err)
			os.Exit(1)
		}
		if err := os.MkdirAll(*outputPath, 0755); err != nil {
			fmt.Println("Failed:", err)
			os.Exit(1)
		}
		if err := ioutil.WriteFile(filepath.Join(*outputPath, "dlc.dat"), protoBytes, 0644); err != nil {
			fmt.Println("Failed:", err)
			os.Exit(1)
		} else {
			fmt.Printf("dlc.dat has been generated successfully in '%s'. You can rename 'dlc.dat' to 'geosite.dat' and use it in V2Ray.\n", *outputPath)
		}
	}

	// Generate plaintext list files
	if filePlainTextBytesMap, err := listInfoMap.ToPlainText(exportListsSlice); err == nil {
		for filename, plaintextBytes := range filePlainTextBytesMap {
			filename += ".txt"
			if err := ioutil.WriteFile(filepath.Join(*outputPath, filename), plaintextBytes, 0644); err != nil {
				fmt.Println("Failed:", err)
				os.Exit(1)
			} else {
				fmt.Printf("%s has been generated successfully in '%s'.\n", filename, *outputPath)
			}
		}
	} else {
		fmt.Println("Failed:", err)
		os.Exit(1)
	}

	// Generate gfwlist.txt
	if gfwlistBytes, err := listInfoMap.ToGFWList(*toGFWList); err == nil {
		if f, err := os.OpenFile(filepath.Join(*outputPath, "gfwlist.txt"), os.O_RDWR|os.O_CREATE, 0644); err != nil {
			fmt.Println("Failed:", err)
			os.Exit(1)
		} else {
			encoder := base64.NewEncoder(base64.StdEncoding, f)
			defer encoder.Close()
			if _, err := encoder.Write(gfwlistBytes); err != nil {
				fmt.Println("Failed:", err)
				os.Exit(1)
			}
			fmt.Printf("gfwlist.txt has been generated successfully in '%s'.\n", *outputPath)
		}
	} else {
		fmt.Println("Failed:", err)
		os.Exit(1)
	}
}
