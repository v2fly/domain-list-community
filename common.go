package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"v2ray.com/core/common"
)

type fileName string

type attribute string

// GetDataDir returns the path to the "data" directory used to generate lists.
// Usage order:
// 1. The datapath that user set when running the program
// 2. The default path "./data" (data directory in the current working directory) if exists
// 3. The path to the data directory in "GOPATH/src/modulepath"
func GetDataDir() string {
	var dir string
	defaultDataDir := filepath.Join("./", "data")
	if *dataPath != defaultDataDir { // Use dataPath option if is set by user
		dir = *dataPath
		fmt.Printf("Use domain list files in '%s' directory.\n", dir)
		return dir
	}
	if _, err := os.Stat(defaultDataDir); !os.IsNotExist(err) { // Use "./data" directory if exists
		dir = defaultDataDir
		fmt.Printf("Use domain list files in '%s' directory.\n", dir)
		return dir
	}
	goPath := common.GetGOPATH()
	pwd, wdErr := os.Getwd()
	if wdErr != nil {
		fmt.Println("Failed: cannot get current working directory", wdErr)
		fmt.Println("Please run in the root path to the project")
		os.Exit(1)
	}
	moduleName, mnErr := common.GetModuleName(pwd)
	if mnErr != nil {
		fmt.Println("Failed: cannot get module name", mnErr)
		os.Exit(1)
	}
	modulePath := filepath.Join(strings.Split(moduleName, "/")...)
	dir = filepath.Join(goPath, "src", modulePath, "data")
	fmt.Println("Use $GOPATH:", goPath)
	fmt.Printf("Use domain list files in '%s' directory.\n", dir)
	return dir
}

// isEmpty checks if the rule that has been trimmed out spaces is empty
func isEmpty(s string) bool {
	return len(strings.TrimSpace(s)) == 0
}

// removeComment removes comments in the rule
func removeComment(line string) string {
	idx := strings.Index(line, "#")
	if idx == -1 {
		return line
	}
	return strings.TrimSpace(line[:idx])
}
