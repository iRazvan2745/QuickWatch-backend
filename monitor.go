package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
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
}

func NewMonitor(url string, checkIntervalSeconds int, maxHistory int, retries int, retryDelay time.Duration, client *http.Client) *Monitor {
	return &Monitor{
		URL:               url,
		Status:            true,
		History:           []string{},
		CheckInterval:     time.Duration(checkIntervalSeconds) * time.Second,
		MaxHistoryEntries: maxHistory,
		Client:            client,
		Retries:           retries,
		RetryDelay:        retryDelay,
	}
}

// Check performs an HTTP GET request to the monitor's URL with retries and measures response time.
func (m *Monitor) Check(ctx context.Context) (bool, int, time.Duration) {
	var (
		status     bool
		statusCode int
		duration   time.Duration
	)

	for attempt := 0; attempt <= m.Retries; attempt++ {
		start := time.Now()
		req, err := http.NewRequestWithContext(ctx, "GET", m.URL, nil)
		if err != nil {
			log.Printf("Error creating request for URL %s: %v", m.URL, err)
			return false, 0, 0
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
			return status, statusCode, duration
		}

		if attempt < m.Retries {
			time.Sleep(m.RetryDelay)
		}
	}

	return status, statusCode, duration
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

// Run starts the monitoring loop, checking the URL at each interval and triggering alerts as needed.
func (m *Monitor) Run(ctx context.Context, alertFunc func(string)) {
	ticker := time.NewTicker(m.CheckInterval)
	defer ticker.Stop() // Ensure the ticker is stopped when Run exits

	for {
		select {
		case <-ctx.Done():
			log.Printf("Stopping monitor for URL: %s", m.URL)
			return
		case <-ticker.C:
			checkCtx, cancel := context.WithTimeout(ctx, m.CheckInterval)
			status, code, duration := m.Check(checkCtx)
			cancel()

			m.mu.Lock()
			previousStatus := m.Status
			m.Status = status // This line is crucial
			m.LastStatusCode = code
			m.LastResponseTime = duration
			m.LastChecked = time.Now()

			if status {
				m.UptimeCount++
			} else {
				m.DowntimeCount++
			}

			m.mu.Unlock()

			if status != previousStatus {
				if !status {
					alertFunc(fmt.Sprintf("ALERT: %s is down! (HTTP Code: %d)", m.URL, code))
					m.AddToHistory("DOWN", code, duration)
				} else {
					alertFunc(fmt.Sprintf("INFO: %s is back up! (HTTP Code: %d)", m.URL, code))
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
	}
}

// GetHistory returns the history of the monitor.
func (m *Monitor) GetHistory() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.History
}
