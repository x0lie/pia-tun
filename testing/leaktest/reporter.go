package main

import (
	"sync"
	"time"
)

// TestResults represents the final output of all leak tests
type TestResults struct {
	StartTime           time.Time              `json:"start_time"`
	EndTime             time.Time              `json:"end_time"`
	Duration            string                 `json:"duration"`
	TotalAttempts       int                    `json:"total_attempts"`
	SuccessfulAttempts  int                    `json:"successful_attempts"`
	FailedAttempts      int                    `json:"failed_attempts"`
	LeaksDetected       int                    `json:"leaks_detected"`
	RealIP              string                 `json:"real_ip,omitempty"`
	ProtocolsTest       []string               `json:"protocols_tested"`
	AttemptsPerProtocol map[string]int         `json:"attempts_per_protocol"`
	LeaksPerProtocol    map[string]int         `json:"leaks_per_protocol"`
	LeakDetails         []LeakDetail           `json:"leak_details,omitempty"`
	Summary             string                 `json:"summary"`
}

// LeakDetail provides information about a detected leak
type LeakDetail struct {
	Timestamp string `json:"timestamp"`
	Protocol  string `json:"protocol"`
	Endpoint  string `json:"endpoint"`
	IP        string `json:"ip,omitempty"`
	Message   string `json:"message"`
}

// Stats represents current testing statistics
type Stats struct {
	TotalAttempts      int
	SuccessfulAttempts int
	LeaksDetected      int
}

// ResultsCollector collects and aggregates test results
type ResultsCollector struct {
	mu                  sync.Mutex
	startTime           time.Time
	realIP              string
	totalAttempts       int
	successfulAttempts  int
	failedAttempts      int
	leaksDetected       int
	attemptsPerProtocol map[string]int
	leaksPerProtocol    map[string]int
	leakDetails         []LeakDetail
	protocols           map[string]bool
}

// NewResultsCollector creates a new results collector
func NewResultsCollector(realIP string) *ResultsCollector {
	return &ResultsCollector{
		startTime:           time.Now(),
		realIP:              realIP,
		attemptsPerProtocol: make(map[string]int),
		leaksPerProtocol:    make(map[string]int),
		leakDetails:         make([]LeakDetail, 0),
		protocols:           make(map[string]bool),
	}
}

// Add adds a test result to the collector
func (rc *ResultsCollector) Add(result TestResult) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	rc.totalAttempts++
	rc.attemptsPerProtocol[result.Protocol]++
	rc.protocols[result.Protocol] = true

	if result.Success {
		rc.successfulAttempts++
	} else {
		rc.failedAttempts++
	}

	if result.Leaked {
		rc.leaksDetected++
		rc.leaksPerProtocol[result.Protocol]++

		// Record leak details (limit to first 100 to avoid huge output)
		if len(rc.leakDetails) < 100 {
			detail := LeakDetail{
				Timestamp: result.Timestamp.Format(time.RFC3339),
				Protocol:  result.Protocol,
				Endpoint:  result.Endpoint,
				IP:        result.IP,
			}

			if result.IP != "" && rc.realIP != "" && result.IP == rc.realIP {
				detail.Message = "Real IP leaked"
			} else {
				detail.Message = "Connection succeeded (killswitch bypassed)"
			}

			rc.leakDetails = append(rc.leakDetails, detail)
		}
	}
}

// GetStats returns current statistics (thread-safe)
func (rc *ResultsCollector) GetStats() Stats {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	return Stats{
		TotalAttempts:      rc.totalAttempts,
		SuccessfulAttempts: rc.successfulAttempts,
		LeaksDetected:      rc.leaksDetected,
	}
}

// Finalize generates the final test results
func (rc *ResultsCollector) Finalize(duration time.Duration) *TestResults {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	endTime := time.Now()

	// Build protocols list
	protocolsList := make([]string, 0, len(rc.protocols))
	for p := range rc.protocols {
		protocolsList = append(protocolsList, p)
	}

	// Generate summary
	summary := "No leaks detected"
	if rc.leaksDetected > 0 {
		summary = "LEAKS DETECTED - Killswitch may be compromised"
	} else if rc.successfulAttempts > 0 {
		summary = "WARNING: Some connections succeeded but no real IP leaks detected"
	}

	return &TestResults{
		StartTime:           rc.startTime,
		EndTime:             endTime,
		Duration:            duration.String(),
		TotalAttempts:       rc.totalAttempts,
		SuccessfulAttempts:  rc.successfulAttempts,
		FailedAttempts:      rc.failedAttempts,
		LeaksDetected:       rc.leaksDetected,
		RealIP:              rc.realIP,
		ProtocolsTest:       protocolsList,
		AttemptsPerProtocol: rc.attemptsPerProtocol,
		LeaksPerProtocol:    rc.leaksPerProtocol,
		LeakDetails:         rc.leakDetails,
		Summary:             summary,
	}
}
