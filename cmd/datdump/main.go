package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	router "github.com/v2fly/v2ray-core/v5/app/router/routercommon"
	"google.golang.org/protobuf/proto"
)

var (
	inputData   = flag.String("inputdata", "dlc.dat", "Name of the geosite dat file")
	outputDir   = flag.String("outputdir", "./", "Directory to place all generated files")
	exportLists = flag.String("exportlists", "", "Lists to be exported, separated by ',' (empty for _all_)")
)

type DomainRule struct {
	Type  string
	Value string
	Attrs []string
}

type DomainList struct {
	Name  string
	Rules []DomainRule
}

func (d *DomainRule) domain2String() string {
	dstring := d.Type + ":" + d.Value
	if len(d.Attrs) != 0 {
		dstring += ":@" + strings.Join(d.Attrs, ",@")
	}
	return dstring
}

func loadGeosite(path string) (*[]DomainList, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read geosite file: %w", err)
	}
	vgeositeList := new(router.GeoSiteList)
	if err := proto.Unmarshal(data, vgeositeList); err != nil {
		return nil, fmt.Errorf("failed to unmarshal: %w", err)
	}
	domainLists := make([]DomainList, 0, len(vgeositeList.Entry))
	for _, vsite := range vgeositeList.Entry {
		domainList := DomainList{
			Name:  strings.ToUpper(vsite.CountryCode),
			Rules: make([]DomainRule, 0, len(vsite.Domain)),
		}
		for _, vdomain := range vsite.Domain {
			rule := DomainRule{Value: vdomain.Value}
			switch vdomain.Type {
			case router.Domain_RootDomain:
				rule.Type = "domain"
			case router.Domain_Regex:
				rule.Type = "regexp"
			case router.Domain_Plain:
				rule.Type = "keyword"
			case router.Domain_Full:
				rule.Type = "full"
			default:
				return nil, fmt.Errorf("invalid rule type: %+v", vdomain.Type)
			}
			for _, vattr := range vdomain.Attribute {
				rule.Attrs = append(rule.Attrs, vattr.Key)
			}
			domainList.Rules = append(domainList.Rules, rule)
		}
		domainLists = append(domainLists, domainList)
	}
	return &domainLists, nil
}

func exportSite(name string, domainLists *[]DomainList) error {
	exist := false
	for _, domainList := range *domainLists {
		if domainList.Name == strings.ToUpper(name) {
			if len(domainList.Rules) == 0 {
				return fmt.Errorf("list '%s' is empty", name)
			}
			exist = true
			file, err := os.Create(filepath.Join(*outputDir, name+".yml"))
			if err != nil {
				return err
			}
			defer file.Close()
			w := bufio.NewWriter(file)
			fmt.Fprintf(w, "%s:\n", name)
			for _, domain := range domainList.Rules {
				fmt.Fprintf(w, "  - %q\n", domain.domain2String())
			}
			return w.Flush()
		}
	}
	if !exist {
		return fmt.Errorf("list '%s' does not exist", name)
	}
	return nil
}

func exportAll(filename string, domainLists *[]DomainList) error {
	file, err := os.Create(filepath.Join(*outputDir, filename))
	if err != nil {
		return err
	}
	defer file.Close()
	w := bufio.NewWriter(file)
	w.WriteString("lists:\n")
	for _, domainList := range *domainLists {
		fmt.Fprintf(w, "  - name: %s\n", strings.ToLower(domainList.Name))
		fmt.Fprintf(w, "    length: %d\n", len(domainList.Rules))
		w.WriteString("    rules:\n")
		for _, domain := range domainList.Rules {
			fmt.Fprintf(w, "      - %q\n", domain.domain2String())
		}
	}
	return w.Flush()
}

func main() {
	flag.Parse()

	// Create output directory if not exist
	if _, err := os.Stat(*outputDir); os.IsNotExist(err) {
		if mkErr := os.MkdirAll(*outputDir, 0755); mkErr != nil {
			fmt.Println("Failed to create output directory:", mkErr)
			os.Exit(1)
		}
	}

	fmt.Printf("Loading %s...\n", *inputData)
	domainLists, err := loadGeosite(*inputData)
	if err != nil {
		fmt.Println("Failed to loadGeosite:", err)
		os.Exit(1)
	}

	var exportListSlice []string
	for raw := range strings.SplitSeq(*exportLists, ",") {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			exportListSlice = append(exportListSlice, trimmed)
		}
	}
	if len(exportListSlice) == 0 {
		exportListSlice = []string{"_all_"}
	}

	for _, eplistname := range exportListSlice {
		if strings.EqualFold(eplistname, "_all_") {
			if err := exportAll(filepath.Base(*inputData)+"_plain.yml", domainLists); err != nil {
				fmt.Println("Failed to exportAll:", err)
				continue
			}
		} else {
			if err := exportSite(eplistname, domainLists); err != nil {
				fmt.Println("Failed to exportSite:", err)
				continue
			}
		}
		fmt.Printf("list: '%s' has been exported successfully.\n", eplistname)
	}
}
