package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const historyDataPath = "domain lists"

func newHistoryRepository(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for history integration tests")
	}
	for _, name := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LC_ALL", "C")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_AUTHOR_NAME", "Domain History Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "domain-history@example.test")
	t.Setenv("GIT_COMMITTER_NAME", "Domain History Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "domain-history@example.test")
	repository := t.TempDir()
	historyGit(t, repository, "init", "--initial-branch=main", "--template=")
	t.Chdir(repository)
	if err := os.Mkdir(historyDataPath, 0755); err != nil {
		t.Fatal(err)
	}
	return repository
}

func historyGit(t *testing.T, repository string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository, "-c", "core.autocrlf=false", "-c", "commit.gpgsign=false"}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", arguments, err, output)
	}
	return string(output)
}

func writeHistoryList(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(historyDataPath, "sample"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func commitHistoryList(t *testing.T, repository, date string) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_DATE", date)
	t.Setenv("GIT_COMMITTER_DATE", date)
	historyGit(t, repository, "add", ".")
	historyGit(t, repository, "commit", "--quiet", "-m", "Update domain history fixture")
}

func assertHistoryDate(t *testing.T, entry *domainEntry, date string) {
	t.Helper()
	want, err := time.Parse(time.RFC3339, date)
	if err != nil {
		t.Fatal(err)
	}
	if !entry.FirstSeen.Equal(want) {
		t.Errorf("%s first seen = %v, want %v", entry.Domain, entry.FirstSeen, want)
	}
}

func TestLoadHistoryIncludesMergeOnlyAdditions(t *testing.T) {
	repository := newHistoryRepository(t)
	writeHistoryList(t, "base.com\n")
	commitHistoryList(t, repository, "2020-01-01T00:00:00Z")
	historyGit(t, repository, "switch", "-c", "side")
	writeHistoryList(t, "side.com\n")
	commitHistoryList(t, repository, "2021-01-01T00:00:00Z")
	historyGit(t, repository, "switch", "main")
	writeHistoryList(t, "main.com\n")
	commitHistoryList(t, repository, "2022-01-01T00:00:00Z")
	command := exec.Command("git", "-C", repository, "merge", "--no-ff", "--no-commit", "side")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "CONFLICT") {
		t.Fatalf("fixture merge should conflict: err=%v\n%s", err, output)
	}
	writeHistoryList(t, "base.com\nmain.com\nside.com\nmerge-only.com\n")
	commitHistoryList(t, repository, "2023-01-01T00:00:00Z")
	domains := map[string]*domainEntry{
		"base.com":       {Domain: "base.com"},
		"main.com":       {Domain: "main.com"},
		"side.com":       {Domain: "side.com"},
		"merge-only.com": {Domain: "merge-only.com"},
	}
	if err := loadHistory(context.Background(), historyDataPath, domains); err != nil {
		t.Fatalf("loadHistory() got unexpected error: %v", err)
	}
	assertHistoryDate(t, domains["base.com"], "2020-01-01T00:00:00Z")
	assertHistoryDate(t, domains["main.com"], "2022-01-01T00:00:00Z")
	assertHistoryDate(t, domains["side.com"], "2021-01-01T00:00:00Z")
	assertHistoryDate(t, domains["merge-only.com"], "2023-01-01T00:00:00Z")
}

func TestLoadHistoryIncludesPrunedMergedBranch(t *testing.T) {
	repository := newHistoryRepository(t)
	writeHistoryList(t, "base.com\n")
	commitHistoryList(t, repository, "2020-01-01T00:00:00Z")
	historyGit(t, repository, "switch", "-c", "side")
	writeHistoryList(t, "base.com\nshared.com\n")
	commitHistoryList(t, repository, "2021-01-01T00:00:00Z")
	historyGit(t, repository, "switch", "main")
	writeHistoryList(t, "base.com\nshared.com\n")
	commitHistoryList(t, repository, "2022-01-01T00:00:00Z")
	t.Setenv("GIT_AUTHOR_DATE", "2023-01-01T00:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2023-01-01T00:00:00Z")
	historyGit(t, repository, "merge", "--no-ff", "--no-edit", "side")
	domains := map[string]*domainEntry{"shared.com": {Domain: "shared.com"}}
	if err := loadHistory(context.Background(), historyDataPath, domains); err != nil {
		t.Fatalf("loadHistory() got unexpected error: %v", err)
	}
	assertHistoryDate(t, domains["shared.com"], "2021-01-01T00:00:00Z")
}

func TestLoadHistoryReportsReadErrors(t *testing.T) {
	repository := newHistoryRepository(t)
	writeHistoryList(t, "base.com\n"+strings.Repeat("a", 5*1024*1024)+"\n")
	commitHistoryList(t, repository, "2020-01-01T00:00:00Z")
	err := loadHistory(context.Background(), historyDataPath, map[string]*domainEntry{"base.com": {Domain: "base.com"}})
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Fatalf("loadHistory() error = %v, want %v", err, bufio.ErrTooLong)
	}
}

func TestLoadHistoryRejectsShallowRepository(t *testing.T) {
	repository := newHistoryRepository(t)
	writeHistoryList(t, "base.com\n")
	commitHistoryList(t, repository, "2020-01-01T00:00:00Z")
	writeHistoryList(t, "base.com\nadded.com\n")
	commitHistoryList(t, repository, "2021-01-01T00:00:00Z")
	clone := filepath.Join(t.TempDir(), "shallow")
	historyGit(t, repository, "clone", "--no-local", "--depth=1", repository, clone)
	t.Chdir(clone)
	err := loadHistory(context.Background(), historyDataPath, map[string]*domainEntry{"added.com": {Domain: "added.com"}})
	if err == nil || !strings.Contains(err.Error(), "complete git history is required") {
		t.Fatalf("loadHistory() error = %v, want shallow repository rejection", err)
	}
}
