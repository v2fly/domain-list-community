package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"
)

var (
	dataPath  = flag.String("datapath", "data", "Directory containing domain lists")
	outputDir = flag.String("outputdir", "domain-audit", "Directory for audit reports")
	workers   = flag.Int("workers", 12, "Maximum concurrent domain checks")
	interval  = flag.Duration("interval", 500*time.Millisecond, "Minimum interval between requests to each HTTP host")
	timeout   = flag.Duration("timeout", 5*time.Hour, "Maximum audit duration, including retries")
)

func run(ctx context.Context) error {
	if *workers < 1 || *interval <= 0 || *timeout <= 0 {
		return fmt.Errorf("workers, interval and timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	data, err := loadInventory(*dataPath)
	if err != nil {
		return fmt.Errorf("load domain lists: %w", err)
	}
	fmt.Printf("Auditing %d registration domains from %d literal rules\n", len(data.Domains), data.LiteralRules)
	if err := loadHistory(ctx, *dataPath, data.Domains); err != nil {
		return fmt.Errorf("load domain history: %w", err)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	return auditDomains(ctx, data, client, *interval, *workers, *outputDir)
}

func auditDomains(ctx context.Context, data *inventory, client *http.Client, interval time.Duration, workers int, outputDir string) error {
	names := sortedDomains(data.Domains)
	results := make([]checkResult, len(names))
	checker, err := newChecker(ctx, client, interval)
	if err != nil {
		bootstrapErr := fmt.Errorf("load RDAP bootstrap: %w", err)
		for index, name := range names {
			results[index] = checkResult{domainEntry: data.Domains[name], Status: statusUnknown, Detail: bootstrapErr.Error()}
		}
		return errors.Join(bootstrapErr, writeReports(outputDir, data, results))
	}
	jobs := make(chan int)
	var group sync.WaitGroup
	var completed atomic.Int64
	for range workers {
		group.Go(func() {
			for index := range jobs {
				results[index] = checker.check(ctx, data.Domains[names[index]])
				if count := completed.Add(1); count%1000 == 0 || count == int64(len(names)) {
					fmt.Printf("Checked %d/%d registration domains\n", count, len(names))
				}
			}
		})
	}
	for index := range names {
		jobs <- index
	}
	close(jobs)
	group.Wait()
	reportErr := writeReports(outputDir, data, results)
	if ctx.Err() != nil {
		return errors.Join(fmt.Errorf("audit incomplete: %w", ctx.Err()), reportErr)
	}
	return reportErr
}

func main() {
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "[Error] %v\n", err)
		os.Exit(1)
	}
}
