package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

type Monitor struct {
	URL               string
	Status            bool
	LastStatusCode    int
	LastResponseTime  time.Duration
	LastChecked       time.Time
	History           []string
	mu                sync.Mutex // Mutex to protect shared data
	CheckInterval     time.Duration
	MaxHistoryEntries int
	Client            *http.Client
	Retries           int
	RetryDelay        time.Duration
	UptimeCount       int
	DowntimeCount     int
	UptimeHistory     []UptimeRecord
	UptimePercentage  float64
	UptimeFile        string
}

type UptimeRecord struct {
	Timestamp time.Time
	IsUp      bool
}

func NewMonitor(url string, maxHistory int, retries int, retryDelay time.Duration, client *http.Client) *Monitor {
	// Load .env file
	if err := godotenv.Load(); err != nil {
		log.Printf("Error loading .env file: %v", err)
	}

	// Get check interval from .env
	checkIntervalSeconds, err := strconv.Atoi(os.Getenv("CHECK_INTERVAL_SECONDS"))
	if err != nil {
		log.Printf("Error parsing CHECK_INTERVAL_SECONDS, using default of 60 seconds: %v", err)
		checkIntervalSeconds = 60
	}

	m := &Monitor{
		URL:               url,
		Status:            true,
		History:           []string{},
		CheckInterval:     time.Duration(checkIntervalSeconds) * time.Second,
		MaxHistoryEntries: maxHistory,
		Client:            client,
		Retries:           retries,
		RetryDelay:        retryDelay,
		UptimeFile:        filepath.Join("uptime", url+".json"),
	}

	m.loadUptimeData()
	return m
}

func (m *Monitor) loadUptimeData() {
	data, err := ioutil.ReadFile(m.UptimeFile)
	if err == nil {
		var uptimeData struct {
			UptimePercentage float64 `json:"uptime_percentage"`
		}
		if err := json.Unmarshal(data, &uptimeData); err == nil {
			m.UptimePercentage = uptimeData.UptimePercentage
		}
	} else {
		// If the file does not exist, create it with initial data
		m.saveUptimeData()
	}
}

func (m *Monitor) saveUptimeData() {
	data, err := json.Marshal(struct {
		UptimePercentage float64 `json:"uptime_percentage"`
	}{
		UptimePercentage: m.UptimePercentage,
	})
	if err == nil {
		_ = ioutil.WriteFile(m.UptimeFile, data, 0644)
	}
}

func (m *Monitor) updateUptimePercentage() {
	totalChecks := m.UptimeCount + m.DowntimeCount
	if totalChecks > 0 {
		m.UptimePercentage = float64(m.UptimeCount) / float64(totalChecks) * 100
	} else {
		m.UptimePercentage = 0
	}
	m.saveUptimeData()
}

// Improved Check method with context cancellation handling
func (m *Monitor) Check(ctx context.Context) (bool, int, time.Duration, error) {
	var (
		status     bool
		statusCode int
		duration   time.Duration
	)

	for attempt := 0; attempt <= m.Retries; attempt++ {
		start := time.Now()
		req, err := http.NewRequestWithContext(ctx, "GET", m.URL, nil)
		if err != nil {
			return false, 0, 0, fmt.Errorf("creating request: %w", err)
		}

		resp, err := m.Client.Do(req)
		duration = time.Since(start)
		if err != nil {
			log.Printf("Error checking URL %s (attempt %d/%d): %v", m.URL, attempt+1, m.Retries+1, err)
			status = false
			statusCode = 0
		} else {
			statusCode = resp.StatusCode
			status = resp.StatusCode == http.StatusOK
			resp.Body.Close()
		}

		if status {
			return status, statusCode, duration, nil
		}

		if attempt < m.Retries {
			select {
			case <-ctx.Done():
				return false, 0, 0, ctx.Err()
			case <-time.After(m.RetryDelay):
			}
		}
	}

	return status, statusCode, duration, nil
}

// AddToHistory adds a new entry to the monitor's history, maintaining the maximum history size.
func (m *Monitor) AddToHistory(status string, code int, duration time.Duration) {
	m.mu.Lock() // Lock to ensure thread-safe access to History
	defer m.mu.Unlock()

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	logEntry := fmt.Sprintf("%s: %s (HTTP Code: %d, Response Time: %v)", timestamp, status, code, duration)
	m.History = append(m.History, logEntry)

	// Maintain maximum history size
	if len(m.History) > m.MaxHistoryEntries {
		m.History = m.History[1:]
	}

	file, err := os.OpenFile("monitor.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Error opening log file: %v", err)
		return
	}
	defer file.Close()

	if _, err := file.WriteString(logEntry + "\n"); err != nil {
		log.Printf("Error writing to log file: %v", err)
	}
}

// Run method with enhanced context and error handling
func (m *Monitor) Run(ctx context.Context, alertFunc func(string)) {
	// Initial check on launch
	m.performCheck(ctx, alertFunc)

	ticker := time.NewTicker(m.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("Stopping monitor for URL: %s", m.URL)
			return
		case <-ticker.C:
			m.performCheck(ctx, alertFunc)
		}
	}
}

// Extracted performCheck for cleaner Run method
func (m *Monitor) performCheck(ctx context.Context, alertFunc func(string)) {
	status, code, duration, err := m.Check(ctx)
	if err != nil {
		log.Printf("Check failed for URL %s: %v", m.URL, err)
		return
	}

	m.mu.Lock()
	previousStatus := m.Status
	m.Status = status
	m.LastStatusCode = code
	m.LastResponseTime = duration
	m.LastChecked = time.Now()

	if status {
		m.UptimeCount++
	} else {
		m.DowntimeCount++
	}

	m.UptimeHistory = append(m.UptimeHistory, UptimeRecord{
		Timestamp: time.Now(),
		IsUp:      status,
	})

	// Keep only the last 30 days of history
	thirtyDaysAgo := time.Now().AddDate(0, 0, -30)
	for len(m.UptimeHistory) > 0 && m.UptimeHistory[0].Timestamp.Before(thirtyDaysAgo) {
		m.UptimeHistory = m.UptimeHistory[1:]
	}
	m.mu.Unlock()

	m.updateUptimePercentage()

	if status != previousStatus {
		if !status {
			alertFunc(fmt.Sprintf("ALERT: %s is down! (HTTP Code: %d)", m.URL, code))
			m.AddToHistory("DOWN", code, duration)
		} else {
			alertFunc(fmt.Sprintf("INFO: %s is back up! (HTTP Code: %d)", m.URL, code))
			fmt.Printf("INFO: %s is back up! (HTTP Code: %d)", m.URL, code)
			m.AddToHistory("UP", code, duration)
		}
	} else {
		if status {
			alertFunc(fmt.Sprintf("INFO: %s is up! (HTTP Code: %d, Response Time: %v)", m.URL, code, duration))
			m.AddToHistory("UP", code, duration)
		} else {
			alertFunc(fmt.Sprintf("ALERT: %s is still down! (HTTP Code: %d)", m.URL, code))
			m.AddToHistory("DOWN", code, duration)
		}
	}
}

// GetHistory returns the history of the monitor.
func (m *Monitor) GetHistory() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.History
}
