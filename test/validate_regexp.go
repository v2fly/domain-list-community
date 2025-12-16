package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	dataPath = flag.String("datapath", "./data", "Path to the 'data' directory")
)

type RegexpError struct {
	File    string
	Line    int
	Pattern string
	Error   string
}

func main() {
	flag.Parse()

	var errors []RegexpError

	err := filepath.Walk(*dataPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		fileErrors, err := validateFile(path)
		if err != nil {
			return err
		}
		errors = append(errors, fileErrors...)
		return nil
	})

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error walking directory: %v\n", err)
		os.Exit(1)
	}

	if len(errors) > 0 {
		fmt.Println("Invalid regexp patterns found:")
		fmt.Println(strings.Repeat("=", 60))
		for _, e := range errors {
			fmt.Printf("\nFile: %s\n", e.File)
			fmt.Printf("Line: %d\n", e.Line)
			fmt.Printf("Pattern: %s\n", e.Pattern)
			fmt.Printf("Error: %s\n", e.Error)
		}
		fmt.Println(strings.Repeat("=", 60))
		fmt.Printf("\nTotal invalid regexp patterns: %d\n", len(errors))
		os.Exit(1)
	}

	fmt.Println("All regexp patterns are valid.")
}

func validateFile(path string) ([]RegexpError, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var errors []RegexpError
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Remove comments
		if idx := strings.Index(line, "#"); idx != -1 {
			line = strings.TrimSpace(line[:idx])
		}

		if len(line) == 0 {
			continue
		}

		// Parse entry: first part before space is domain spec
		parts := strings.Split(line, " ")
		domainSpec := parts[0]

		// Check if it's a regexp entry
		if strings.HasPrefix(strings.ToLower(domainSpec), "regexp:") {
			pattern := domainSpec[7:]
			pattern = strings.ToLower(pattern)

			_, err := regexp.Compile(pattern)
			if err != nil {
				errors = append(errors, RegexpError{
					File:    path,
					Line:    lineNum,
					Pattern: pattern,
					Error:   err.Error(),
				})
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return errors, nil
}
