package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var validDomainRegexp = regexp.MustCompile(`^[a-z0-9_\-\.]+$`)

func validateFile(t *testing.T, path string) {
	list, err := Load(path)
	if err != nil {
		t.Errorf("❌ [Parse Error] File: %s | Error: %v", path, err)
		return
	}

	for _, entry := range list.Entry {
		switch entry.Type {
		case "regexp":
			if _, err := regexp.Compile(entry.Value); err != nil {
				t.Errorf("❌ [Invalid Regex] File: %s | Rule: '%s' | Error: %v",
					path, entry.Value, err)
			}
		case "domain", "full":
			if strings.Contains(entry.Value, "http://") || strings.Contains(entry.Value, "https://") {
				t.Errorf("❌ [Protocol Error] File: %s | Rule: '%s' | Remove http/s prefix",
					path, entry.Value)
			}
			if strings.Contains(entry.Value, " ") {
				t.Errorf("❌ [Spaces Error] File: %s | Rule: '%s' | Domain contains spaces",
					path, entry.Value)
			}
			if !validDomainRegexp.MatchString(entry.Value) {
				t.Errorf("❌ [Invalid Char] File: %s | Rule: '%s' | Contains illegal characters",
					path, entry.Value)
			}
		case "include":
			targetPath := filepath.Join("data", entry.Value)
			if _, err := os.Stat(targetPath); os.IsNotExist(err) {
				t.Errorf("❌ [Missing Include] File: %s | Target: '%s' does not exist",
					path, entry.Value)
			}
		}
	}
}

func TestDataSyntax(t *testing.T) {
	changedFilesEnv := os.Getenv("CHANGED_FILES")

	if changedFilesEnv != "" {
		rawFiles := strings.Split(changedFilesEnv, ",")
		var targetFiles []string

		for _, f := range rawFiles {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			if strings.HasPrefix(f, "data/") || strings.HasPrefix(f, "data\\") {
				if _, err := os.Stat(f); err == nil {
					targetFiles = append(targetFiles, f)
				}
			}
		}

		total := len(targetFiles)
		if total == 0 {
			t.Log("ℹ️ CHANGED_FILES was set, but no valid 'data/' files were found to test.")
			return
		}

		t.Logf("📝 Validating %d specific file(s):", total)
		displayLimit := 5
		for i, f := range targetFiles {
			if i >= displayLimit {
				t.Logf("   ... and %d more files.", total-i)
				break
			}
			t.Logf("   - %s", f)
		}
		t.Log("---------------------------------------------------")

		for _, f := range targetFiles {
			validateFile(t, f)
		}

	} else {
		dataDir := "data"
		if _, err := os.Stat(dataDir); os.IsNotExist(err) {
			t.Fatalf("Data directory '%s' not found.", dataDir)
		}

		t.Log("---------------------------------------------------")
		t.Log("🌍 CHANGED_FILES not set. Running FULL syntax check on 'data/'...")
		t.Log("---------------------------------------------------")

		count := 0
		err := filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			count++
			validateFile(t, path)
			return nil
		})

		if err != nil {
			t.Fatalf("Failed to walk data directory: %v", err)
		}
		t.Logf("✅ Checked %d files in total.", count)
	}
}
