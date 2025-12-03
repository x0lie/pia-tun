package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const version = "1.0.0"

func main() {
	// CLI flags
	var (
		duration    = flag.Duration("duration", 60*time.Second, "How long to run tests")
		concurrency = flag.Int("concurrency", 50, "Number of concurrent test goroutines")
		interval    = flag.Duration("interval", 100*time.Millisecond, "Interval between test attempts per goroutine")
		realIP      = flag.String("real-ip", "", "Real IP address (to detect leaks)")
		protocols   = flag.String("protocols", "http,https,dns,udp,bypass", "Comma-separated protocols to test")
		output      = flag.String("output", "", "Output file for JSON results (default: stdout)")
		quiet       = flag.Bool("quiet", false, "Suppress progress output")
		showVersion = flag.Bool("version", false, "Show version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("leaktest version %s\n", version)
		os.Exit(0)
	}

	// Parse protocols
	protocolList := parseProtocols(*protocols)
	if len(protocolList) == 0 {
		log.Fatal("No valid protocols specified")
	}

	// Validate real IP if provided
	if *realIP != "" && !isValidIP(*realIP) {
		log.Fatalf("Invalid real IP address: %s", *realIP)
	}

	// Create tester
	tester := NewLeakTester(LeakTesterConfig{
		Duration:    *duration,
		Concurrency: *concurrency,
		Interval:    *interval,
		RealIP:      *realIP,
		Protocols:   protocolList,
		Quiet:       *quiet,
	})

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		if !*quiet {
			fmt.Fprintln(os.Stderr, "\nReceived interrupt, stopping tests...")
		}
		tester.Stop()
	}()

	// Run tests
	if !*quiet {
		fmt.Fprintf(os.Stderr, "Starting leak tests for %s with %d concurrent workers\n", *duration, *concurrency)
		fmt.Fprintf(os.Stderr, "Testing protocols: %s\n", strings.Join(protocolList, ", "))
		if *realIP != "" {
			fmt.Fprintf(os.Stderr, "Real IP: %s\n", *realIP)
		}
		fmt.Fprintln(os.Stderr, "")
	}

	results := tester.Run()

	// Output results
	jsonData, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		log.Fatalf("Failed to marshal results: %v", err)
	}

	if *output != "" {
		if err := os.WriteFile(*output, jsonData, 0644); err != nil {
			log.Fatalf("Failed to write output file: %v", err)
		}
		if !*quiet {
			fmt.Fprintf(os.Stderr, "Results written to %s\n", *output)
		}
	} else {
		fmt.Println(string(jsonData))
	}

	// Exit with error if leaks detected
	if results.LeaksDetected > 0 {
		os.Exit(1)
	}
}

func parseProtocols(protocolStr string) []string {
	parts := strings.Split(protocolStr, ",")
	var protocols []string
	validProtocols := map[string]bool{
		"http":   true,
		"https":  true,
		"dns":    true,
		"udp":    true,
		"bypass": true,
		"icmp":   true,
	}

	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if validProtocols[p] {
			protocols = append(protocols, p)
		}
	}

	return protocols
}

func isValidIP(ip string) bool {
	// Simple validation - check format
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts {
		if len(part) == 0 || len(part) > 3 {
			return false
		}
	}
	return true
}
