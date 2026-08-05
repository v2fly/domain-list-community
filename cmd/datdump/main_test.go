package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	router "github.com/v2fly/v2ray-core/v5/app/router/routercommon"
	"google.golang.org/protobuf/proto"
)

// mainExitEnv stores the missing input path for the re-executed test binary.
const mainExitEnv = "DATDUMP_TEST_MAIN_EXIT"

func TestLoadGeosite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.dat")
	writeTestDat(t, path, testSites()...)

	gs, err := loadGeosite(path)
	if err != nil {
		t.Fatalf("loadGeosite(%q) got unexpected error: %v", path, err)
	}
	if len(gs.Sites) != 2 {
		t.Fatalf("loadGeosite(%q) sites = %v, want 2 sites", path, gs.Sites)
	}
	for name, wantIdx := range map[string]int{"CN": 0, "GOOGLE": 1} {
		if idx, ok := gs.SiteIdx[name]; !ok || idx != wantIdx {
			t.Errorf("loadGeosite(%q) index of %q = %d/%v, want %d", path, name, idx, ok, wantIdx)
		}
	}

	t.Run("missing file", func(t *testing.T) {
		if _, err := loadGeosite(filepath.Join(dir, "missing.dat")); err == nil {
			t.Error("loadGeosite(\"missing.dat\") = nil, want file reading error")
		}
	})
	t.Run("invalid content", func(t *testing.T) {
		invalid := filepath.Join(dir, "invalid.dat")
		if err := os.WriteFile(invalid, []byte{0xff, 0xff, 0xff, 0xff}, 0644); err != nil {
			t.Fatalf("failed to write invalid dat: %v", err)
		}
		if _, err := loadGeosite(invalid); err == nil {
			t.Error("loadGeosite(\"invalid.dat\") = nil, want unmarshaling error")
		}
	})
}

func TestDomain2Builder(t *testing.T) {
	testCases := []struct {
		name    string
		domain  *router.Domain
		want    string
		wantErr bool
	}{
		{name: "domain", domain: &router.Domain{Type: router.Domain_RootDomain, Value: "example.com"}, want: "domain:example.com"},
		{name: "full", domain: &router.Domain{Type: router.Domain_Full, Value: "www.example.com"}, want: "full:www.example.com"},
		{name: "keyword", domain: &router.Domain{Type: router.Domain_Plain, Value: "example"}, want: "keyword:example"},
		{name: "regexp", domain: &router.Domain{Type: router.Domain_Regex, Value: `^example\.com$`}, want: `regexp:^example\.com$`},
		{
			name:   "attributes",
			domain: &router.Domain{Type: router.Domain_RootDomain, Value: "example.com", Attribute: testAttrs("ads", "cn")},
			want:   "domain:example.com:@ads,@cn",
		},
		{name: "invalid type", domain: &router.Domain{Type: router.Domain_Type(99), Value: "example.com"}, wantErr: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			err := domain2Builder(tc.domain, &b)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("domain2Builder(%+v) = %q, want error", tc.domain, b.String())
				}
				return
			}
			if err != nil {
				t.Fatalf("domain2Builder(%+v) got unexpected error: %v", tc.domain, err)
			}
			if b.String() != tc.want {
				t.Errorf("domain2Builder(%+v) = %q, want %q", tc.domain, b.String(), tc.want)
			}
		})
	}
}

func TestExportSite(t *testing.T) {
	outPath := t.TempDir()
	setFlag(t, outputDir, outPath)
	gs := &GeoSites{Sites: testSites(), SiteIdx: map[string]int{"CN": 0, "GOOGLE": 1}}

	if err := exportSite("cn", gs); err != nil {
		t.Fatalf("exportSite(\"cn\") got unexpected error: %v", err)
	}
	want := strings.Join([]string{
		`"cn":`,
		`  - "domain:example.cn"`,
		`  - "full:www.example.cn:@ads,@cn"`,
		`  - "keyword:examplecn"`,
		`  - "regexp:^example\\.cn$"`,
		"",
	}, "\n")
	assertFileContent(t, filepath.Join(outPath, "cn.yml"), want)

	t.Run("missing list", func(t *testing.T) {
		if err := exportSite("missing", gs); err == nil {
			t.Error("exportSite(\"missing\") = nil, want list not found error")
		}
	})
	t.Run("empty list", func(t *testing.T) {
		empty := &GeoSites{Sites: []*router.GeoSite{{CountryCode: "EMPTY"}}, SiteIdx: map[string]int{"EMPTY": 0}}
		if err := exportSite("empty", empty); err == nil {
			t.Error("exportSite(\"empty\") = nil, want empty list error")
		}
	})
	t.Run("blocked output file", func(t *testing.T) {
		if err := os.Mkdir(filepath.Join(outPath, "google.yml"), 0755); err != nil {
			t.Fatalf("failed to create blocking directory: %v", err)
		}
		if err := exportSite("google", gs); err == nil {
			t.Error("exportSite(\"google\") = nil, want file creating error")
		}
	})
	t.Run("invalid domain type", func(t *testing.T) {
		if err := exportSite("invalid", invalidGeoSites()); err == nil {
			t.Error("exportSite(\"invalid\") = nil, want invalid rule type error")
		}
	})
}

func TestExportAll(t *testing.T) {
	outPath := t.TempDir()
	setFlag(t, outputDir, outPath)
	gs := &GeoSites{Sites: testSites(), SiteIdx: map[string]int{"CN": 0, "GOOGLE": 1}}

	if err := exportAll("all.yml", gs); err != nil {
		t.Fatalf("exportAll(\"all.yml\") got unexpected error: %v", err)
	}
	want := strings.Join([]string{
		"lists:",
		`  - name: "cn"`,
		"    length: 4",
		"    rules:",
		`      - "domain:example.cn"`,
		`      - "full:www.example.cn:@ads,@cn"`,
		`      - "keyword:examplecn"`,
		`      - "regexp:^example\\.cn$"`,
		`  - name: "google"`,
		"    length: 1",
		"    rules:",
		`      - "domain:google.com"`,
		"",
	}, "\n")
	assertFileContent(t, filepath.Join(outPath, "all.yml"), want)

	t.Run("blocked output file", func(t *testing.T) {
		if err := os.Mkdir(filepath.Join(outPath, "blocked.yml"), 0755); err != nil {
			t.Fatalf("failed to create blocking directory: %v", err)
		}
		if err := exportAll("blocked.yml", gs); err == nil {
			t.Error("exportAll(\"blocked.yml\") = nil, want file creating error")
		}
	})
	t.Run("invalid domain type", func(t *testing.T) {
		if err := exportAll("invalid.yml", invalidGeoSites()); err == nil {
			t.Error("exportAll(\"invalid.yml\") = nil, want invalid rule type error")
		}
	})
}

func TestRun(t *testing.T) {
	datPath := filepath.Join(t.TempDir(), "test.dat")
	writeTestDat(t, datPath, testSites()...)

	t.Run("all lists by default", func(t *testing.T) {
		outPath := filepath.Join(t.TempDir(), "output") // Not existing yet
		setRunFlags(t, datPath, outPath, "")
		if err := run(); err != nil {
			t.Fatalf("run() got unexpected error: %v", err)
		}
		assertFileExists(t, filepath.Join(outPath, "test.dat_plain.yml"))
	})
	t.Run("selected lists", func(t *testing.T) {
		outPath := t.TempDir()
		setRunFlags(t, datPath, outPath, "cn, ,_all_")
		if err := run(); err != nil {
			t.Fatalf("run() got unexpected error: %v", err)
		}
		assertFileExists(t, filepath.Join(outPath, "cn.yml"))
		assertFileExists(t, filepath.Join(outPath, "test.dat_plain.yml"))
	})
	t.Run("invalid output directory", func(t *testing.T) {
		outPath := t.TempDir()
		blocking := filepath.Join(outPath, "blocking")
		if err := os.WriteFile(blocking, nil, 0644); err != nil {
			t.Fatalf("failed to create blocking file: %v", err)
		}
		setRunFlags(t, datPath, filepath.Join(blocking, "output"), "")
		if err := run(); err == nil {
			t.Error("run() = nil, want output directory error")
		}
	})
	t.Run("missing input dat", func(t *testing.T) {
		setRunFlags(t, filepath.Join(t.TempDir(), "missing.dat"), t.TempDir(), "")
		if err := run(); err == nil {
			t.Error("run() = nil, want input dat error")
		}
	})
	t.Run("failed list export", func(t *testing.T) {
		setRunFlags(t, datPath, t.TempDir(), "missing")
		if err := run(); err == nil {
			t.Error("run() = nil, want list export error")
		}
	})
	t.Run("failed export of all lists", func(t *testing.T) {
		outPath := t.TempDir()
		if err := os.Mkdir(filepath.Join(outPath, "test.dat_plain.yml"), 0755); err != nil {
			t.Fatalf("failed to create blocking directory: %v", err)
		}
		setRunFlags(t, datPath, outPath, "_ALL_")
		if err := run(); err == nil {
			t.Error("run() = nil, want export error")
		}
	})
}

func TestMainExportsLists(t *testing.T) {
	datPath := filepath.Join(t.TempDir(), "test.dat")
	writeTestDat(t, datPath, testSites()...)
	outPath := t.TempDir()
	setRunFlags(t, datPath, outPath, "")

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"datdump", "--inputdata=" + datPath, "--outputdir=" + outPath, "--exportlists=cn"}
	main()

	assertFileExists(t, filepath.Join(outPath, "cn.yml"))
}

// TestMainExitsOnError re-executes the test binary, because main() terminates
// the process when the export fails.
func TestMainExitsOnError(t *testing.T) {
	if missingPath := os.Getenv(mainExitEnv); missingPath != "" {
		os.Args = []string{"datdump", "--inputdata=" + missingPath}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainExitsOnError$")
	cmd.Env = append(os.Environ(), mainExitEnv+"="+filepath.Join(t.TempDir(), "missing.dat"))
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

func setRunFlags(t *testing.T, input, output, exports string) {
	t.Helper()
	setFlag(t, inputData, input)
	setFlag(t, outputDir, output)
	setFlag(t, exportLists, exports)
}

func testAttrs(keys ...string) []*router.Domain_Attribute {
	attrs := make([]*router.Domain_Attribute, 0, len(keys))
	for _, key := range keys {
		attrs = append(attrs, &router.Domain_Attribute{
			Key:        key,
			TypedValue: &router.Domain_Attribute_BoolValue{BoolValue: true},
		})
	}
	return attrs
}

func testSites() []*router.GeoSite {
	return []*router.GeoSite{
		{CountryCode: "CN", Domain: []*router.Domain{
			{Type: router.Domain_RootDomain, Value: "example.cn"},
			{Type: router.Domain_Full, Value: "www.example.cn", Attribute: testAttrs("ads", "cn")},
			{Type: router.Domain_Plain, Value: "examplecn"},
			{Type: router.Domain_Regex, Value: `^example\.cn$`},
		}},
		{CountryCode: "GOOGLE", Domain: []*router.Domain{
			{Type: router.Domain_RootDomain, Value: "google.com"},
		}},
	}
}

func invalidGeoSites() *GeoSites {
	return &GeoSites{
		Sites: []*router.GeoSite{{
			CountryCode: "INVALID",
			Domain:      []*router.Domain{{Type: router.Domain_Type(99), Value: "example.com"}},
		}},
		SiteIdx: map[string]int{"INVALID": 0},
	}
}

func writeTestDat(t *testing.T, path string, sites ...*router.GeoSite) {
	t.Helper()
	data, err := proto.Marshal(&router.GeoSiteList{Entry: sites})
	if err != nil {
		t.Fatalf("failed to marshal test dat: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write test dat: %v", err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %q: %v", path, err)
	}
	if string(got) != want {
		t.Errorf("%q = %q, want %q", path, got, want)
	}
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("%q has not been generated: %v", path, err)
	}
}
