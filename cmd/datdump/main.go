package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/v2fly/domain-list-community/internal/dlc"
	router "github.com/v2fly/v2ray-core/v5/app/router/routercommon"
	"google.golang.org/protobuf/proto"
)

var (
	inputData   = flag.String("inputdata", "dlc.dat", "Name of the geosite dat file")
	outputDir   = flag.String("outputdir", "./", "Directory to place all generated files")
	exportLists = flag.String("exportlists", "", "Lists to be exported, separated by ',' (empty for _all_)")
)

type GeoSites struct {
	Sites   []*router.GeoSite
	SiteIdx map[string]int
}

func loadGeosite(path string) (*GeoSites, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read geosite file: %w", err)
	}
	vgeositeList := new(router.GeoSiteList)
	if err := proto.Unmarshal(data, vgeositeList); err != nil {
		return nil, fmt.Errorf("failed to unmarshal: %w", err)
	}
	gs := &GeoSites{Sites: vgeositeList.Entry}
	gs.SiteIdx = make(map[string]int, len(gs.Sites))
	for i, site := range gs.Sites {
		gs.SiteIdx[strings.ToUpper(site.CountryCode)] = i
	}
	return gs, nil
}

func domain2Builder(d *router.Domain, b *strings.Builder) error {
	switch d.Type {
	case router.Domain_RootDomain:
		b.WriteString(dlc.RuleTypeDomain)
	case router.Domain_Full:
		b.WriteString(dlc.RuleTypeFullDomain)
	case router.Domain_Plain:
		b.WriteString(dlc.RuleTypeKeyword)
	case router.Domain_Regex:
		b.WriteString(dlc.RuleTypeRegexp)
	default:
		return fmt.Errorf("invalid rule type: %+v", d.Type)
	}
	b.WriteByte(':')
	b.WriteString(d.Value)
	for i, attr := range d.Attribute {
		if i == 0 {
			b.WriteByte(':')
		} else {
			b.WriteByte(',')
		}
		b.WriteByte('@')
		b.WriteString(attr.Key)
	}
	return nil
}

func exportSite(name string, gs *GeoSites) error {
	idx, ok := gs.SiteIdx[strings.ToUpper(name)]
	if !ok {
		return fmt.Errorf("list %q does not exist", name)
	}
	vDomains := gs.Sites[idx].Domain
	if len(vDomains) == 0 {
		return fmt.Errorf("list %q is empty", name)
	}
	file, err := os.Create(filepath.Join(*outputDir, name+".yml"))
	if err != nil {
		return err
	}
	defer file.Close()
	w := bufio.NewWriter(file)
	fmt.Fprintf(w, "%s:\n", name)
	var b strings.Builder
	b.Grow(64)
	for _, vdomain := range vDomains {
		b.Reset()
		if err := domain2Builder(vdomain, &b); err != nil {
			return err
		}
		fmt.Fprintf(w, "  - %q\n", b.String())
	}
	return w.Flush()
}

func exportAll(filename string, gs *GeoSites) error {
	file, err := os.Create(filepath.Join(*outputDir, filename))
	if err != nil {
		return err
	}
	defer file.Close()
	w := bufio.NewWriter(file)
	w.WriteString("lists:\n")
	var b strings.Builder
	b.Grow(64)
	for _, site := range gs.Sites {
		fmt.Fprintf(w, "  - name: %q\n", strings.ToLower(site.CountryCode))
		fmt.Fprintf(w, "    length: %d\n", len(site.Domain))
		w.WriteString("    rules:\n")
		for _, vdomain := range site.Domain {
			b.Reset()
			if err := domain2Builder(vdomain, &b); err != nil {
				return err
			}
			fmt.Fprintf(w, "      - %q\n", b.String())
		}
	}
	return w.Flush()
}

func run() error {
	// Make sure output directory exists
	if err := os.MkdirAll(*outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	fmt.Printf("loading source data %q...\n", *inputData)
	geoSites, err := loadGeosite(*inputData)
	if err != nil {
		return fmt.Errorf("failed to loadGeosite: %w", err)
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
			if err := exportAll(filepath.Base(*inputData)+"_plain.yml", geoSites); err != nil {
				fmt.Printf("[Error] failed to exportAll: %v\n", err)
				continue
			}
		} else {
			if err := exportSite(eplistname, geoSites); err != nil {
				fmt.Printf("[Error] failed to exportSite: %v\n", err)
				continue
			}
		}
		fmt.Printf("list: %q has been exported successfully\n", eplistname)
	}
	return nil
}

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Printf("[Fatal] critical error: %v\n", err)
		os.Exit(1)
	}
}
