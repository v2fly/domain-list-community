package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/v2fly/domain-list-community/internal/dlc"
	router "github.com/v2fly/v2ray-core/v5/app/router/routercommon"
	"google.golang.org/protobuf/proto"
)

// mainExitEnv marks the re-executed test binary that has to run main().
const mainExitEnv = "DLC_TEST_MAIN_EXIT"

// datList is a list and its rules read back from a generated dat file.
type datList struct {
	name  string
	rules []string
}

func TestParseEntry(t *testing.T) {
	testCases := []struct {
		name      string
		typ       string
		rule      string
		wantPlain string
		wantAffs  []string
		wantErr   bool
	}{
		{name: "domain", typ: "domain", rule: "Example.COM", wantPlain: "domain:example.com"},
		{name: "sorted attrs", typ: "full", rule: "a.example.com @cn @ads", wantPlain: "full:a.example.com:@ads,@cn"},
		{name: "duplicated attrs", typ: "domain", rule: "example.com @ads @ads", wantPlain: "domain:example.com:@ads"},
		{name: "affiliations", typ: "domain", rule: "example.com &other @ads", wantPlain: "domain:example.com:@ads", wantAffs: []string{"OTHER"}},
		{name: "regexp", typ: "regexp", rule: `^example\.com$`, wantPlain: `regexp:^example\.com$`},
		{name: "tld", typ: "domain", rule: "google", wantPlain: "domain:google"},
		{name: "hyphen inside", typ: "domain", rule: "my-example.com", wantPlain: "domain:my-example.com"},
		{name: "keyword allows partial", typ: "keyword", rule: "-ads-", wantPlain: "keyword:-ads-"},
		{name: "invalid regexp", typ: "regexp", rule: "^example(", wantErr: true},
		{name: "empty rule", typ: "domain", rule: "   ", wantErr: true},
		{name: "unknown type", typ: "prefix", rule: "example.com", wantErr: true},
		{name: "invalid domain", typ: "domain", rule: "exa_mple.com @ads", wantErr: true},
		{name: "invalid keyword", typ: "keyword", rule: "ad_s", wantErr: true},
		{name: "overlong domain", typ: "domain", rule: strings.Repeat("a.", 127) + "com", wantErr: true},
		{name: "invalid attr", typ: "domain", rule: "example.com @a_ds", wantErr: true},
		{name: "invalid affiliation", typ: "domain", rule: "example.com &other_list", wantErr: true},
		{name: "empty label", typ: "domain", rule: "example..com", wantErr: true},
		{name: "trailing dot", typ: "full", rule: "example.com.", wantErr: true},
		{name: "leading dot", typ: "domain", rule: ".example.com", wantErr: true},
		{name: "leading hyphen", typ: "domain", rule: "-example.com", wantErr: true},
		{name: "trailing hyphen", typ: "full", rule: "example-.com", wantErr: true},
		{name: "overlong label", typ: "domain", rule: strings.Repeat("a", 64) + ".com", wantErr: true},
		{name: "empty attr", typ: "domain", rule: "example.com @", wantErr: true},
		{name: "empty affiliation", typ: "domain", rule: "example.com &", wantErr: true},
		{name: "unknown field", typ: "domain", rule: "example.com ads", wantErr: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			entry, affs, err := parseEntry(tc.typ, tc.rule)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseEntry(%q, %q) = %q, want error", tc.typ, tc.rule, entry.Plain)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseEntry(%q, %q) got unexpected error: %v", tc.typ, tc.rule, err)
			}
			if entry.Plain != tc.wantPlain {
				t.Errorf("parseEntry(%q, %q) = %q, want %q", tc.typ, tc.rule, entry.Plain, tc.wantPlain)
			}
			if len(affs) != len(tc.wantAffs) {
				t.Fatalf("parseEntry(%q, %q) affiliations = %v, want %v", tc.typ, tc.rule, affs, tc.wantAffs)
			}
			for i, aff := range affs {
				if aff != tc.wantAffs[i] {
					t.Errorf("parseEntry(%q, %q) affiliations = %v, want %v", tc.typ, tc.rule, affs, tc.wantAffs)
				}
			}
		})
	}
}

func TestParseInclusion(t *testing.T) {
	testCases := []struct {
		name     string
		rule     string
		wantSrc  string
		wantMust []string
		wantBan  []string
		wantErr  bool
	}{
		{name: "plain", rule: "other-list", wantSrc: "OTHER-LIST"},
		{name: "filters", rule: "other @ads @-cn", wantSrc: "OTHER", wantMust: []string{"ads"}, wantBan: []string{"cn"}},
		{name: "empty attr", rule: "other @", wantErr: true},
		{name: "empty ban attr", rule: "other @-", wantErr: true},
		{name: "empty rule", rule: " ", wantErr: true},
		{name: "invalid name", rule: "other@list", wantErr: true},
		{name: "unknown field", rule: "other list", wantErr: true},
		{name: "affiliation", rule: "other &another", wantErr: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			inc, err := parseInclusion(tc.rule)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseInclusion(%q) = %+v, want error", tc.rule, inc)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseInclusion(%q) got unexpected error: %v", tc.rule, err)
			}
			if inc.Source != tc.wantSrc {
				t.Errorf("parseInclusion(%q) source = %q, want %q", tc.rule, inc.Source, tc.wantSrc)
			}
			if len(inc.MustAttrs) != len(tc.wantMust) || len(inc.BanAttrs) != len(tc.wantBan) {
				t.Fatalf("parseInclusion(%q) filters = %v/%v, want %v/%v", tc.rule, inc.MustAttrs, inc.BanAttrs, tc.wantMust, tc.wantBan)
			}
			for i, attr := range inc.MustAttrs {
				if attr != tc.wantMust[i] {
					t.Errorf("parseInclusion(%q) must attrs = %v, want %v", tc.rule, inc.MustAttrs, tc.wantMust)
				}
			}
			for i, attr := range inc.BanAttrs {
				if attr != tc.wantBan[i] {
					t.Errorf("parseInclusion(%q) ban attrs = %v, want %v", tc.rule, inc.BanAttrs, tc.wantBan)
				}
			}
		})
	}
}

func TestPolishList(t *testing.T) {
	rules := []struct{ typ, rule string }{
		{"domain", "example.com @cn"},
		{"domain", "sub.example.com"},      // Redundant, no attribute
		{"full", "www.example.com @cn"},    // Redundant, same attribute
		{"full", "example.com"},            // Redundant, no attribute
		{"full", "example.org"},            // Kept, no parent domain rule
		{"domain", "ads.example.com @ads"}, // Kept, different attribute
		{"keyword", "example"},
	}
	roughMap := make(map[string]*Entry, len(rules))
	for _, r := range rules {
		entry, _, err := parseEntry(r.typ, r.rule)
		if err != nil {
			t.Fatalf("parseEntry(%q, %q) got unexpected error: %v", r.typ, r.rule, err)
		}
		roughMap[entry.Plain] = entry
	}
	want := []string{"domain:ads.example.com:@ads", "domain:example.com:@cn", "full:example.org", "keyword:example"}
	assertPlains(t, "polishList", polishList(roughMap), want)
}

// TestResolveSelectiveInclusion makes sure that selective inclusion does not
// lose rules which are redundant in the source list only.
func TestResolveSelectiveInclusion(t *testing.T) {
	dataPath := t.TempDir()
	files := map[string]string{
		"source": "domain:example.com @cn\nfull:mail.example.com\ndomain:sub.example.com\ndomain:example.org @ads\n",
		"banned": "include:source @-cn\n",
		"must":   "include:source @ads\n",
		"full":   "include:source\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dataPath, name), []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test data %q: %v", name, err)
		}
	}

	processor := &Processor{parsedListByName: make(map[string]*ParsedList)}
	for name := range files {
		if err := processor.loadData(strings.ToUpper(name), filepath.Join(dataPath, name)); err != nil {
			t.Fatalf("loadData(%q) got unexpected error: %v", name, err)
		}
	}
	for name := range files {
		if _, err := processor.resolveList(strings.ToUpper(name)); err != nil {
			t.Fatalf("resolveList(%q) got unexpected error: %v", name, err)
		}
	}

	assertList(t, processor, "SOURCE", []string{"domain:example.com:@cn", "domain:example.org:@ads"})
	assertList(t, processor, "FULL", []string{"domain:example.com:@cn", "domain:example.org:@ads"})
	assertList(t, processor, "MUST", []string{"domain:example.org:@ads"})
	assertList(t, processor, "BANNED", []string{"domain:example.org:@ads", "domain:sub.example.com", "full:mail.example.com"})
}

func TestResolveCircularInclusion(t *testing.T) {
	dataPath := t.TempDir()
	files := map[string]string{
		"first":  "domain:example.com\ninclude:second\n",
		"second": "include:first\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dataPath, name), []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test data %q: %v", name, err)
		}
	}
	processor := &Processor{parsedListByName: make(map[string]*ParsedList)}
	for name := range files {
		if err := processor.loadData(strings.ToUpper(name), filepath.Join(dataPath, name)); err != nil {
			t.Fatalf("loadData(%q) got unexpected error: %v", name, err)
		}
	}
	if _, err := processor.resolveList("FIRST"); err == nil {
		t.Fatal("resolveList(\"FIRST\") = nil, want circular inclusion error")
	}
}

func TestValidateChars(t *testing.T) {
	testCases := []struct {
		name     string
		validate func(string) bool
		input    string
		want     bool
	}{
		{name: "domain chars", validate: validateDomainChars, input: "my-example.com", want: true},
		{name: "empty domain chars", validate: validateDomainChars, input: "", want: false},
		{name: "uppercase domain chars", validate: validateDomainChars, input: "Example.com", want: false},
		{name: "domain name", validate: validateDomainName, input: "a.example.com", want: true},
		{name: "empty domain name", validate: validateDomainName, input: "", want: false},
		{name: "attr chars", validate: validateAttrChars, input: "!ads1", want: true},
		{name: "empty attr chars", validate: validateAttrChars, input: "", want: false},
		{name: "uppercase attr chars", validate: validateAttrChars, input: "Ads", want: false},
		{name: "site name", validate: validateSiteName, input: "GEOLOCATION-!CN", want: true},
		{name: "empty site name", validate: validateSiteName, input: "", want: false},
		{name: "lowercase site name", validate: validateSiteName, input: "cn", want: false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.validate(tc.input); got != tc.want {
				t.Errorf("validate(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestLoadData(t *testing.T) {
	dataPath := t.TempDir()
	content := strings.Join([]string{
		"# A comment line",
		"",
		"   ",
		"example.com # Rule without type falls back to domain",
		"full:mail.example.com @cn &affiliated",
		"include:other @ads",
		"keyword:example",
		`regexp:^ads\.example\.com$`,
	}, "\n")
	writeTestData(t, dataPath, map[string]string{"test": content})

	processor := &Processor{parsedListByName: make(map[string]*ParsedList)}
	if err := processor.loadData("TEST", filepath.Join(dataPath, "test")); err != nil {
		t.Fatalf("loadData(\"TEST\") got unexpected error: %v", err)
	}
	pl := processor.parsedListByName["TEST"]
	assertPlains(t, "TEST", pl.Entries, []string{
		"domain:example.com",
		"full:mail.example.com:@cn",
		"keyword:example",
		`regexp:^ads\.example\.com$`,
	})
	if len(pl.Inclusions) != 1 || pl.Inclusions[0].Source != "OTHER" {
		t.Errorf("TEST inclusions = %+v, want one inclusion of %q", pl.Inclusions, "OTHER")
	}
	// Affiliated entries are also added to the affiliated list
	apl, exist := processor.parsedListByName["AFFILIATED"]
	if !exist {
		t.Fatal("list \"AFFILIATED\" does not exist")
	}
	assertPlains(t, "AFFILIATED", apl.Entries, []string{"full:mail.example.com:@cn"})
}

func TestLoadDataErrors(t *testing.T) {
	dataPath := t.TempDir()
	files := map[string]string{
		"invalid-entry":     "domain:exa_mple.com\n",
		"invalid-inclusion": "include:other@list\n",
	}
	writeTestData(t, dataPath, files)
	for name := range files {
		t.Run(name, func(t *testing.T) {
			processor := &Processor{parsedListByName: make(map[string]*ParsedList)}
			if err := processor.loadData("TEST", filepath.Join(dataPath, name)); err == nil {
				t.Errorf("loadData(%q) = nil, want parsing error", name)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		processor := &Processor{parsedListByName: make(map[string]*ParsedList)}
		if err := processor.loadData("TEST", filepath.Join(dataPath, "missing")); err == nil {
			t.Error("loadData(\"missing\") = nil, want file opening error")
		}
	})
}

func TestResolveMissingList(t *testing.T) {
	processor := &Processor{parsedListByName: make(map[string]*ParsedList)}
	if _, err := processor.resolveList("MISSING"); err == nil {
		t.Error("resolveList(\"MISSING\") = nil, want list not found error")
	}
}

func TestResolveEmptyList(t *testing.T) {
	dataPath := t.TempDir()
	writeTestData(t, dataPath, map[string]string{"empty": "# Only a comment\n"})
	processor := &Processor{parsedListByName: make(map[string]*ParsedList)}
	if err := processor.loadData("EMPTY", filepath.Join(dataPath, "empty")); err != nil {
		t.Fatalf("loadData(\"EMPTY\") got unexpected error: %v", err)
	}
	pl, err := processor.resolveList("EMPTY")
	if err != nil {
		t.Fatalf("resolveList(\"EMPTY\") got unexpected error: %v", err)
	}
	if len(pl.FinalEntries) != 0 {
		t.Errorf("EMPTY final entries = %v, want none", pl.FinalEntries)
	}
}

func TestMakeProtoList(t *testing.T) {
	rules := []struct{ typ, rule string }{
		{"domain", "example.com @cn @ads"},
		{"full", "www.example.com"},
		{"keyword", "example"},
		{"regexp", `^example\.com$`},
	}
	entries := make([]*Entry, 0, len(rules))
	for _, r := range rules {
		entry, _, err := parseEntry(r.typ, r.rule)
		if err != nil {
			t.Fatalf("parseEntry(%q, %q) got unexpected error: %v", r.typ, r.rule, err)
		}
		entries = append(entries, entry)
	}

	site := makeProtoList("TEST", entries)
	if site.CountryCode != "TEST" {
		t.Errorf("makeProtoList() country code = %q, want %q", site.CountryCode, "TEST")
	}
	wantTypes := []router.Domain_Type{
		router.Domain_RootDomain,
		router.Domain_Full,
		router.Domain_Plain,
		router.Domain_Regex,
	}
	if len(site.Domain) != len(wantTypes) {
		t.Fatalf("makeProtoList() domains = %v, want %d domains", site.Domain, len(wantTypes))
	}
	for i, pdomain := range site.Domain {
		if pdomain.Type != wantTypes[i] {
			t.Errorf("makeProtoList() domain[%d] type = %v, want %v", i, pdomain.Type, wantTypes[i])
		}
		if pdomain.Value != entries[i].Value {
			t.Errorf("makeProtoList() domain[%d] value = %q, want %q", i, pdomain.Value, entries[i].Value)
		}
	}
	attrs := site.Domain[0].Attribute
	if len(attrs) != 2 || attrs[0].Key != "ads" || attrs[1].Key != "cn" {
		t.Fatalf("makeProtoList() domain[0] attributes = %v, want %v", attrs, []string{"ads", "cn"})
	}
	for _, attr := range attrs {
		if !attr.GetBoolValue() {
			t.Errorf("makeProtoList() attribute %q value = %v, want true", attr.Key, attr.GetBoolValue())
		}
	}
}

func TestLoadTasks(t *testing.T) {
	dataPath := t.TempDir()
	testCases := []struct {
		name    string
		content string
		want    []DatTask
		wantErr bool
	}{
		{
			name:    "valid",
			content: `[{"name":"dlc.dat","mode":"all"},{"name":"cn.dat","mode":"allowlist","lists":["cn"]},{"name":"nocn.dat","mode":"denylist","lists":["cn"]}]`,
			want: []DatTask{
				{Name: "dlc.dat", Mode: ModeAll},
				{Name: "cn.dat", Mode: ModeAllowlist, Lists: []string{"cn"}},
				{Name: "nocn.dat", Mode: ModeDenylist, Lists: []string{"cn"}},
			},
		},
		{name: "invalid json", content: "{", wantErr: true},
		{name: "missing name", content: `[{"mode":"all"}]`, wantErr: true},
		{name: "invalid mode", content: `[{"name":"dlc.dat","mode":"unknown"}]`, wantErr: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dataPath, tc.name+".json")
			if err := os.WriteFile(path, []byte(tc.content), 0644); err != nil {
				t.Fatalf("failed to write test profile %q: %v", path, err)
			}
			tasks, err := loadTasks(path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("loadTasks(%q) = %+v, want error", tc.name, tasks)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadTasks(%q) got unexpected error: %v", tc.name, err)
			}
			if !slices.EqualFunc(tasks, tc.want, func(a, b DatTask) bool {
				return a.Name == b.Name && a.Mode == b.Mode && slices.Equal(a.Lists, b.Lists)
			}) {
				t.Errorf("loadTasks(%q) = %+v, want %+v", tc.name, tasks, tc.want)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		if _, err := loadTasks(filepath.Join(dataPath, "missing.json")); err == nil {
			t.Error("loadTasks(\"missing.json\") = nil, want file opening error")
		}
	})
}

func TestAssembleDat(t *testing.T) {
	outPath := t.TempDir()
	setFlag(t, outputDir, outPath)
	gs := newTestGeoSites("APPLE", "CN", "GOOGLE")
	testCases := []struct {
		name    string
		task    DatTask
		want    []string
		wantErr bool
	}{
		{name: "all", task: DatTask{Name: "All.dat", Mode: ModeAll}, want: []string{"APPLE", "CN", "GOOGLE"}},
		{name: "allowlist", task: DatTask{Name: "allow.dat", Mode: ModeAllowlist, Lists: []string{"google", "cn", "CN"}}, want: []string{"CN", "GOOGLE"}},
		{name: "denylist", task: DatTask{Name: "deny.dat", Mode: ModeDenylist, Lists: []string{"cn", "missing"}}, want: []string{"APPLE", "GOOGLE"}},
		{name: "denylist without valid list", task: DatTask{Name: "nodeny.dat", Mode: ModeDenylist, Lists: []string{"missing"}}, want: []string{"APPLE", "CN", "GOOGLE"}},
		{name: "allowlist with missing list", task: DatTask{Name: "missing.dat", Mode: ModeAllowlist, Lists: []string{"missing"}}, wantErr: true},
		{name: "allowlist without list", task: DatTask{Name: "none.dat", Mode: ModeAllowlist}, wantErr: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := gs.assembleDat(tc.task)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("assembleDat(%+v) = nil, want error", tc.task)
				}
				return
			}
			if err != nil {
				t.Fatalf("assembleDat(%+v) got unexpected error: %v", tc.task, err)
			}
			got := readDatSites(t, filepath.Join(outPath, strings.ToLower(tc.task.Name)))
			if !slices.Equal(got, tc.want) {
				t.Errorf("assembleDat(%+v) = %v, want %v", tc.task, got, tc.want)
			}
		})
	}
}

func TestAssembleDatErrors(t *testing.T) {
	outPath := t.TempDir()
	setFlag(t, outputDir, outPath)
	// A directory occupying the output path makes writing the dat file fail
	if err := os.Mkdir(filepath.Join(outPath, "blocked.dat"), 0755); err != nil {
		t.Fatalf("failed to create blocking directory: %v", err)
	}
	gs := newTestGeoSites("CN")
	if err := gs.assembleDat(DatTask{Name: "Blocked.dat", Mode: ModeAll}); err == nil {
		t.Error("assembleDat() = nil, want file writing error")
	}
	// Invalid UTF-8 in a proto3 string field makes marshaling fail
	invalid := &GeoSites{Sites: []*router.GeoSite{{CountryCode: "\xff"}}}
	if err := invalid.assembleDat(DatTask{Name: "invalid.dat", Mode: ModeAll}); err == nil {
		t.Error("assembleDat() = nil, want marshaling error")
	}
}

func TestWritePlainList(t *testing.T) {
	outPath := t.TempDir()
	setFlag(t, outputDir, outPath)
	entries := []*Entry{{Plain: "domain:example.com"}, {Plain: "full:www.example.com:@ads"}}
	if err := writePlainList("CN", entries); err != nil {
		t.Fatalf("writePlainList(\"CN\") got unexpected error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(outPath, "cn.txt"))
	if err != nil {
		t.Fatalf("failed to read generated list: %v", err)
	}
	want := "domain:example.com\nfull:www.example.com:@ads\n"
	if string(got) != want {
		t.Errorf("writePlainList(\"CN\") wrote %q, want %q", got, want)
	}
	// A directory occupying the output path makes creating the list file fail
	if err := os.Mkdir(filepath.Join(outPath, "blocked.txt"), 0755); err != nil {
		t.Fatalf("failed to create blocking directory: %v", err)
	}
	if err := writePlainList("Blocked", entries); err == nil {
		t.Error("writePlainList(\"Blocked\") = nil, want file creating error")
	}
}

func TestRun(t *testing.T) {
	dataPath := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "output") // Not existing yet
	writeTestData(t, dataPath, map[string]string{
		"cn":      "# Chinese sites\ndomain:example.cn\nfull:www.example.cn @ads\nkeyword:examplecn\n",
		"apple":   "include:cn\ndomain:apple.com &icloud\n",
		"icloud":  "domain:icloud.com\n",
		"google":  "domain:google.com\n",
		"netflix": "domain:netflix.com\n",
		"empty":   "# Nothing here\n",
	})
	setRunFlags(t, dataPath, outPath, "test.dat", "", "cn, ,missing,")

	if err := run(); err != nil {
		t.Fatalf("run() got unexpected error: %v", err)
	}
	// Empty lists are skipped and the remaining ones are sorted by name, so
	// that the generated dat file is reproducible
	want := []datList{
		{name: "APPLE", rules: []string{"domain:apple.com", "domain:example.cn", "full:www.example.cn:@ads", "keyword:examplecn"}},
		{name: "CN", rules: []string{"domain:example.cn", "full:www.example.cn:@ads", "keyword:examplecn"}},
		{name: "GOOGLE", rules: []string{"domain:google.com"}},
		{name: "ICLOUD", rules: []string{"domain:apple.com", "domain:icloud.com"}},
		{name: "NETFLIX", rules: []string{"domain:netflix.com"}},
	}
	got := readDat(t, filepath.Join(outPath, "test.dat"))
	if !slices.EqualFunc(got, want, func(a, b datList) bool {
		return a.name == b.name && slices.Equal(a.rules, b.rules)
	}) {
		t.Errorf("run() generated lists = %+v, want %+v", got, want)
	}
	plain, err := os.ReadFile(filepath.Join(outPath, "cn.txt"))
	if err != nil {
		t.Fatalf("failed to read exported list: %v", err)
	}
	wantPlain := "domain:example.cn\nfull:www.example.cn:@ads\nkeyword:examplecn\n"
	if string(plain) != wantPlain {
		t.Errorf("run() exported list = %q, want %q", plain, wantPlain)
	}
}

func TestRunWithDatProfile(t *testing.T) {
	dataPath := t.TempDir()
	outPath := t.TempDir()
	writeTestData(t, dataPath, map[string]string{
		"cn":     "domain:example.cn\n",
		"google": "domain:google.com\n",
	})
	profile := filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(profile, []byte(`[{"name":"CN.dat","mode":"allowlist","lists":["cn"]}]`), 0644); err != nil {
		t.Fatalf("failed to write test profile: %v", err)
	}
	setRunFlags(t, dataPath, outPath, "dlc.dat", profile, "")

	if err := run(); err != nil {
		t.Fatalf("run() got unexpected error: %v", err)
	}
	if got, want := readDatSites(t, filepath.Join(outPath, "cn.dat")), []string{"CN"}; !slices.Equal(got, want) {
		t.Errorf("run() generated dat = %v, want %v", got, want)
	}
	// Only the dat files defined by the profile are generated
	if _, err := os.Stat(filepath.Join(outPath, "dlc.dat")); !os.IsNotExist(err) {
		t.Errorf("run() generated %q, want it not to exist", "dlc.dat")
	}
}

func TestRunErrors(t *testing.T) {
	t.Run("missing data directory", func(t *testing.T) {
		setRunFlags(t, filepath.Join(t.TempDir(), "missing"), t.TempDir(), "dlc.dat", "", "")
		if err := run(); err == nil {
			t.Error("run() = nil, want data directory error")
		}
	})
	t.Run("invalid list name", func(t *testing.T) {
		dataPath := t.TempDir()
		writeTestData(t, dataPath, map[string]string{"invalid_name": "domain:example.com\n"})
		setRunFlags(t, dataPath, t.TempDir(), "dlc.dat", "", "")
		if err := run(); err == nil {
			t.Error("run() = nil, want invalid list name error")
		}
	})
	t.Run("invalid rule", func(t *testing.T) {
		dataPath := t.TempDir()
		writeTestData(t, dataPath, map[string]string{"cn": "domain:exa_mple.cn\n"})
		setRunFlags(t, dataPath, t.TempDir(), "dlc.dat", "", "")
		if err := run(); err == nil {
			t.Error("run() = nil, want invalid rule error")
		}
	})
	t.Run("circular inclusion", func(t *testing.T) {
		dataPath := t.TempDir()
		writeTestData(t, dataPath, map[string]string{
			"first":  "include:second\n",
			"second": "include:first\n",
		})
		setRunFlags(t, dataPath, t.TempDir(), "dlc.dat", "", "")
		if err := run(); err == nil {
			t.Error("run() = nil, want circular inclusion error")
		}
	})
	t.Run("invalid output directory", func(t *testing.T) {
		dataPath := t.TempDir()
		writeTestData(t, dataPath, map[string]string{"cn": "domain:example.cn\n"})
		outPath := t.TempDir()
		blocking := filepath.Join(outPath, "blocking")
		if err := os.WriteFile(blocking, nil, 0644); err != nil {
			t.Fatalf("failed to create blocking file: %v", err)
		}
		setRunFlags(t, dataPath, filepath.Join(blocking, "output"), "dlc.dat", "", "")
		if err := run(); err == nil {
			t.Error("run() = nil, want output directory error")
		}
	})
	t.Run("missing dat profile", func(t *testing.T) {
		dataPath := t.TempDir()
		writeTestData(t, dataPath, map[string]string{"cn": "domain:example.cn\n"})
		setRunFlags(t, dataPath, t.TempDir(), "dlc.dat", filepath.Join(t.TempDir(), "missing.json"), "")
		if err := run(); err == nil {
			t.Error("run() = nil, want dat profile error")
		}
	})
	t.Run("failed plaintext export", func(t *testing.T) {
		dataPath := t.TempDir()
		writeTestData(t, dataPath, map[string]string{"cn": "domain:example.cn\n"})
		outPath := t.TempDir()
		if err := os.Mkdir(filepath.Join(outPath, "cn.txt"), 0755); err != nil {
			t.Fatalf("failed to create blocking directory: %v", err)
		}
		setRunFlags(t, dataPath, outPath, "dlc.dat", "", "cn")
		if err := run(); err == nil {
			t.Error("run() = nil, want plaintext export error")
		}
	})
	t.Run("failed dat task", func(t *testing.T) {
		dataPath := t.TempDir()
		writeTestData(t, dataPath, map[string]string{"cn": "domain:example.cn\n"})
		profile := filepath.Join(t.TempDir(), "profile.json")
		if err := os.WriteFile(profile, []byte(`[{"name":"none.dat","mode":"allowlist","lists":["missing"]}]`), 0644); err != nil {
			t.Fatalf("failed to write test profile: %v", err)
		}
		setRunFlags(t, dataPath, t.TempDir(), "dlc.dat", profile, "")
		if err := run(); err == nil {
			t.Error("run() = nil, want dat task error")
		}
	})
}

func TestMainGeneratesDat(t *testing.T) {
	dataPath := t.TempDir()
	outPath := t.TempDir()
	writeTestData(t, dataPath, map[string]string{"cn": "domain:example.cn\n"})
	setRunFlags(t, dataPath, outPath, "dlc.dat", "", "")

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"domain-list-community", "--datapath=" + dataPath, "--outputdir=" + outPath, "--outputname=main.dat"}
	main()

	if got, want := readDatSites(t, filepath.Join(outPath, "main.dat")), []string{"CN"}; !slices.Equal(got, want) {
		t.Errorf("main() generated dat = %v, want %v", got, want)
	}
}

// TestMainExitsOnError re-executes the test binary, because main() terminates
// the process when the generation fails.
func TestMainExitsOnError(t *testing.T) {
	if os.Getenv(mainExitEnv) == "1" {
		os.Args = []string{"domain-list-community", "--datapath=" + filepath.Join(t.TempDir(), "missing")}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainExitsOnError$")
	cmd.Env = append(os.Environ(), mainExitEnv+"=1")
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("main() exited with %v, want exit status 1, output:\n%s", err, out)
	}
	if !strings.Contains(string(out), "[Fatal]") {
		t.Errorf("main() output = %q, want it to report a fatal error", out)
	}
}

// setFlag overrides a command line flag for the duration of the test.
func setFlag(t *testing.T, flagValue *string, value string) {
	t.Helper()
	old := *flagValue
	*flagValue = value
	t.Cleanup(func() { *flagValue = old })
}

func setRunFlags(t *testing.T, data, output, name, profile, exports string) {
	t.Helper()
	setFlag(t, dataPath, data)
	setFlag(t, outputDir, output)
	setFlag(t, outputName, name)
	setFlag(t, datProfile, profile)
	setFlag(t, exportLists, exports)
}

func writeTestData(t *testing.T, dataPath string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dataPath, name), []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test data %q: %v", name, err)
		}
	}
}

func newTestGeoSites(names ...string) *GeoSites {
	gs := &GeoSites{
		Sites:   make([]*router.GeoSite, 0, len(names)),
		SiteIdx: make(map[string]int, len(names)),
	}
	for i, name := range names {
		gs.Sites = append(gs.Sites, &router.GeoSite{
			CountryCode: name,
			Domain:      []*router.Domain{{Type: router.Domain_RootDomain, Value: strings.ToLower(name) + ".com"}},
		})
		gs.SiteIdx[name] = i
	}
	return gs
}

// readDatSites returns the names of the lists in a generated dat file, keeping
// the order in which they are stored.
func readDatSites(t *testing.T, path string) []string {
	t.Helper()
	lists := readDat(t, path)
	names := make([]string, 0, len(lists))
	for _, list := range lists {
		names = append(names, list.name)
	}
	return names
}

// readDat returns the lists of a generated dat file, keeping the order in which
// the lists and their rules are stored.
func readDat(t *testing.T, path string) []datList {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read generated dat: %v", err)
	}
	geoSiteList := new(router.GeoSiteList)
	if err := proto.Unmarshal(data, geoSiteList); err != nil {
		t.Fatalf("failed to unmarshal generated dat: %v", err)
	}
	lists := make([]datList, 0, len(geoSiteList.Entry))
	for _, site := range geoSiteList.Entry {
		rules := make([]string, 0, len(site.Domain))
		for _, pdomain := range site.Domain {
			rule := pdomain.Value
			for i, attr := range pdomain.Attribute {
				if i == 0 {
					rule += ":"
				} else {
					rule += ","
				}
				rule += "@" + attr.Key
			}
			switch pdomain.Type {
			case router.Domain_RootDomain:
				rule = dlc.RuleTypeDomain + ":" + rule
			case router.Domain_Full:
				rule = dlc.RuleTypeFullDomain + ":" + rule
			case router.Domain_Plain:
				rule = dlc.RuleTypeKeyword + ":" + rule
			case router.Domain_Regex:
				rule = dlc.RuleTypeRegexp + ":" + rule
			}
			rules = append(rules, rule)
		}
		lists = append(lists, datList{name: site.CountryCode, rules: rules})
	}
	return lists
}

func assertList(t *testing.T, p *Processor, name string, want []string) {
	t.Helper()
	pl, exist := p.parsedListByName[name]
	if !exist {
		t.Fatalf("list %q does not exist", name)
	}
	assertPlains(t, name, pl.FinalEntries, want)
}

func assertPlains(t *testing.T, name string, entries []*Entry, want []string) {
	t.Helper()
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Plain)
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}
