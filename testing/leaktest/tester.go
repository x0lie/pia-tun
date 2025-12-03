package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// LeakTesterConfig holds configuration for the leak tester
type LeakTesterConfig struct {
	Duration    time.Duration
	Concurrency int
	Interval    time.Duration
	RealIP      string
	Protocols   []string
	Quiet       bool
}

// LeakTester orchestrates concurrent leak testing
type LeakTester struct {
	config  LeakTesterConfig
	ctx     context.Context
	cancel  context.CancelFunc
	results *ResultsCollector
}

// NewLeakTester creates a new leak tester instance
func NewLeakTester(config LeakTesterConfig) *LeakTester {
	ctx, cancel := context.WithTimeout(context.Background(), config.Duration)

	return &LeakTester{
		config:  config,
		ctx:     ctx,
		cancel:  cancel,
		results: NewResultsCollector(config.RealIP),
	}
}

// Stop gracefully stops the leak tester
func (lt *LeakTester) Stop() {
	lt.cancel()
}

// Run executes the leak tests and returns results
func (lt *LeakTester) Run() *TestResults {
	startTime := time.Now()

	// Create worker pool
	var wg sync.WaitGroup
	resultsChan := make(chan TestResult, lt.config.Concurrency*10)

	// Start result collector goroutine
	collectorDone := make(chan struct{})
	go func() {
		for result := range resultsChan {
			lt.results.Add(result)
		}
		close(collectorDone)
	}()

	// Start progress reporter if not quiet
	if !lt.config.Quiet {
		go lt.reportProgress()
	}

	// Launch worker goroutines
	for i := 0; i < lt.config.Concurrency; i++ {
		wg.Add(1)
		go lt.worker(i, resultsChan, &wg)
	}

	// Wait for all workers to complete
	wg.Wait()
	close(resultsChan)

	// Wait for result collector to finish
	<-collectorDone

	// Finalize results
	return lt.results.Finalize(time.Since(startTime))
}

// worker is a goroutine that continuously runs tests
func (lt *LeakTester) worker(id int, resultsChan chan<- TestResult, wg *sync.WaitGroup) {
	defer wg.Done()

	ticker := time.NewTicker(lt.config.Interval)
	defer ticker.Stop()

	protocolIndex := 0

	for {
		select {
		case <-lt.ctx.Done():
			return
		case <-ticker.C:
			// Round-robin through protocols
			protocol := lt.config.Protocols[protocolIndex%len(lt.config.Protocols)]
			protocolIndex++

			// Execute test with timeout
			testCtx, cancel := context.WithTimeout(lt.ctx, 5*time.Second)
			result := executeTest(testCtx, protocol, lt.config.RealIP)
			cancel()

			// Send result to collector
			select {
			case resultsChan <- result:
			case <-lt.ctx.Done():
				return
			}
		}
	}
}

// reportProgress shows real-time progress updates
func (lt *LeakTester) reportProgress() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-lt.ctx.Done():
			return
		case <-ticker.C:
			stats := lt.results.GetStats()
			fmt.Fprintf(os.Stderr, "Progress: %d attempts | %d successful | %d LEAKS DETECTED\n",
				stats.TotalAttempts,
				stats.SuccessfulAttempts,
				stats.LeaksDetected)

			if stats.LeaksDetected > 0 {
				fmt.Fprintf(os.Stderr, "⚠️  WARNING: Leaks detected during testing!\n")
			}
		}
	}
}
