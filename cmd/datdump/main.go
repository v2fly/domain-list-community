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

var (
	geositeMap   = make(map[string][]*DomainRule)
	sitenameList []string // keep the order of the input sites
)

func (d *DomainRule) domain2String() string {
	dstring := d.Type + ":" + d.Value
	if len(d.Attrs) != 0 {
		dstring += ":@" + strings.Join(d.Attrs, ",@")
	}
	return dstring
}

func loadGeosite(inputpath string) error {
	data, err := os.ReadFile(inputpath)
	if err != nil {
		return fmt.Errorf("Failed to ReadFile:", err)
	}
	vgeositeList := new(router.GeoSiteList)
	if err := proto.Unmarshal(data, vgeositeList); err != nil {
		return fmt.Errorf("Failed to unmarshal:", err)
	}
	vTypeMap := map[router.Domain_Type]string{
		router.Domain_RootDomain: "domain",
		router.Domain_Regex:      "regexp",
		router.Domain_Plain:      "keyword",
		router.Domain_Full:       "full",
	}
	for _, vsite := range vgeositeList.Entry {
		sitename := strings.ToUpper(vsite.CountryCode)
		siteRules := make([]*DomainRule, 0 ,len(vsite.Domain))
		for _, vdomain := range vsite.Domain {
			rule := &DomainRule{
				Type:  vTypeMap[vdomain.Type],
				Value: vdomain.Value,
			}
			for _, vattr := range vdomain.Attribute {
				rule.Attrs = append(rule.Attrs, vattr.Key)
			}
			siteRules = append(siteRules, rule)
		}

		geositeMap[sitename] = siteRules
		sitenameList = append(sitenameList, sitename)
	}
	return nil
}

func exportSite(name string) error {
	siteDomains, exist := geositeMap[strings.ToUpper(name)]
	if !exist || len(siteDomains) == 0 {
		return fmt.Errorf("list '%s' does not exist or is empty.", name)
	}
	file, err := os.Create(filepath.Join(*outputDir, name + ".yml"))
	if err != nil {
		return err
	}
	defer file.Close()
	w := bufio.NewWriter(file)
	fmt.Fprintf(w, "%s:\n", name)
	for _, domain := range siteDomains {
		fmt.Fprintf(w, "  - %q\n", domain.domain2String())
	}
	return w.Flush()
}

func exportAll(filename string) error {
	file, err := os.Create(filepath.Join(*outputDir, filename))
	if err != nil {
		return err
	}
	defer file.Close()
	w := bufio.NewWriter(file)
	w.WriteString("lists:\n")
	for _, sitename := range sitenameList {
		fmt.Fprintf(w, "  - name: %s\n", sitename)
		fmt.Fprintf(w, "    length: %d\n", len(geositeMap[sitename]))
		w.WriteString("    rules:\n")
		for _, domain := range geositeMap[sitename] {
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
	if err := loadGeosite(*inputData); err != nil {
		fmt.Println("Failed to loadGeosite:", err)
		os.Exit(1)
	}

	var exportListSlice []string
	for _, raw := range strings.Split(*exportLists, ",") {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			exportListSlice = append(exportListSlice, trimmed)
		}
	}
	if len(exportListSlice) == 0 {
		exportListSlice = []string{"_all_"}
	}

	for _, eplistname := range exportListSlice {
		if strings.EqualFold(eplistname, "_all_") {
			if err := exportAll(filepath.Base(*inputData) + "_plain.yml"); err != nil {
				fmt.Println("Failed to exportAll:", err)
				continue
			}
		} else {
			if err := exportSite(eplistname); err != nil {
				fmt.Println("Failed to exportSite:", err)
				continue
			}
		}
		fmt.Printf("list: '%s' has been exported successfully.\n", eplistname)
	}
}
