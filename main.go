package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

type Config struct {
	URLs              []string      `json:"urls"`
	CheckInterval     time.Duration `json:"check_interval"`
	WebhookURL        string        `json:"webhook_url"`
	LogFile           string        `json:"log_file"`
	RetryAttempts     int           `json:"retry_attempts"`
	RetryDelay        time.Duration `json:"retry_delay"`
	Timeout           time.Duration `json:"timeout"`
	AlertThreshold    int           `json:"alert_threshold"`
	RateLimit         rate.Limit    `json:"rate_limit"`
	RateBurst         int           `json:"rate_burst"`
	MetricsPort       string        `json:"metrics_port"`
	HealthCheckPort   string        `json:"health_check_port"`
	TLSCertFile       string        `json:"tls_cert_file"`
	TLSKeyFile        string        `json:"tls_key_file"`
	APIHost           string        `json:"api_host"`
	APIPort           string        `json:"api_port"`
	MaxHistoryEntries int           `json:"max_history_entries"`
}

func main() {
	// Generate .env file
	if err := generateEnvFile(); err != nil {
		log.Fatalf("Error generating .env file: %v", err)
	}

	// Load .env file
	if err := godotenv.Load(); err != nil {
		log.Printf("Error loading .env file: %v", err)
	}

	fmt.Printf("Starting monitoring server...\n")
	config, err := loadConfig()
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
	}
	fmt.Println("Config Loaded!")

	if err := setupLogging(config.LogFile); err != nil {
		log.Fatalf("Error setting up logging: %v", err)
	}
	fmt.Println("Logging Loaded!")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return startHealthCheckServer(ctx, config.HealthCheckPort)
	})

	limiter := rate.NewLimiter(config.RateLimit, config.RateBurst)

	monitors := make([]*Monitor, len(config.URLs))
	for i, url := range config.URLs {
		monitors[i] = NewMonitor(url, config.MaxHistoryEntries, config.RetryAttempts, config.RetryDelay, &http.Client{Timeout: config.Timeout})
	}

	for _, monitor := range monitors {
		m := monitor
		g.Go(func() error {
			m.Run(ctx, func(message string) {
				if err := limiter.Wait(ctx); err != nil {
					log.Printf("Rate limit error: %v", err)
					return
				}
				if err := alertFunc(ctx, message, config.WebhookURL); err != nil {
					log.Printf("Error sending alert: %v", err)
				}
			})
			return nil
		})
	}

	g.Go(func() error {
		return startAPIServer(ctx, monitors, config, limiter)
	})

	log.Println("Monitoring started...")

	select {
	case <-shutdown:
		log.Println("Shutdown signal received. Stopping monitors...")
		cancel()
	case <-ctx.Done():
		log.Println("Context cancelled. Stopping monitors...")
	}

	if err := g.Wait(); err != nil {
		log.Printf("Error during monitoring: %v", err)
	}

	log.Println("Monitoring stopped.")
}

func generateEnvFile() error {
	envContent := `CHECK_INTERVAL_SECONDS=60
DISCORD_WEBHOOK_URL=https://discord.com/api/webhooks/your-webhook-id
API_HOST=0.0.0.0
API_PORT=8080
`
	file, err := os.Create(".env")
	if err != nil {
		return fmt.Errorf("error creating .env file: %w", err)
	}
	defer file.Close()

	if _, err := file.WriteString(envContent); err != nil {
		return fmt.Errorf("error writing to .env file: %w", err)
	}

	return nil
}

func alertFunc(ctx context.Context, message, webhookURL string) error {
	timestamp := time.Now().Format(time.RFC3339)
	log.Printf("%s %s", timestamp, message)
	fmt.Printf("%s \n", message)

	return sendDiscordWebhook(ctx, message, webhookURL)
}

func sendDiscordWebhook(ctx context.Context, message, webhookURL string) error {
	if webhookURL == "" {
		return fmt.Errorf("discord webhook URL not set")
	}

	payload := map[string]string{"content": message}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("error marshaling JSON: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", webhookURL, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return fmt.Errorf("error creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error sending Discord webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unexpected status code from Discord: %d", resp.StatusCode)
	}

	return nil
}

func loadConfig() (*Config, error) {
	checkIntervalStr := os.Getenv("CHECK_INTERVAL_SECONDS")
	checkInterval, err := strconv.Atoi(checkIntervalStr)
	if err != nil {
		return nil, fmt.Errorf("invalid CHECK_INTERVAL_SECONDS: %w", err)
	}

	apiHost := os.Getenv("API_HOST")
	apiPort := os.Getenv("API_PORT")

	return &Config{
		URLs:              []string{"https://example.com", "https://another-example.com"},
		CheckInterval:     time.Duration(checkInterval) * time.Second,
		WebhookURL:        os.Getenv("DISCORD_WEBHOOK_URL"),
		LogFile:           "monitoring.log",
		RetryAttempts:     3,
		RetryDelay:        5 * time.Second,
		Timeout:           30 * time.Second,
		AlertThreshold:    3,
		RateLimit:         rate.Limit(1),
		RateBurst:         5,
		HealthCheckPort:   ":8081",
		APIHost:           apiHost,
		APIPort:           apiPort,
		MaxHistoryEntries: 100,
	}, nil
}

func setupLogging(logFile string) error {
	file, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("error opening log file: %w", err)
	}
	log.SetOutput(file)
	return nil
}

func startHealthCheckServer(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("Error shutting down health check server: %v", err)
		}
	}()

	log.Printf("Starting health check server on %s", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("health check server error: %w", err)
	}

	return nil
}

func startAPIServer(ctx context.Context, monitors []*Monitor, config *Config, limiter *rate.Limiter) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/monitor", getMonitorStatusHandler(monitors))
	mux.HandleFunc("/api/monitor/add", addMonitorHandler(ctx, monitors, config, limiter))
	mux.HandleFunc("/api/chart", getChartDataHandler(monitors))
	mux.HandleFunc("/api/monitor/history", getMonitorHistoryHandler(monitors))

	server := &http.Server{
		Addr:    config.APIHost + ":" + config.APIPort,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("Error shutting down API server: %v", err)
		}
	}()

	log.Printf("Starting API server on http://%s:%s", config.APIHost, config.APIPort)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("API server error: %w", err)
	}

	return nil
}

// Handler for GET /api/monitor
func getMonitorStatusHandler(monitors []*Monitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Access-Control-Allow-Origin", "*")

		type MonitorStatus struct {
			URL          string  `json:"url"`
			Status       bool    `json:"status"`
			StatusCode   int     `json:"status_code"`
			ResponseTime float64 `json:"response_time"` // in milliseconds
			LastChecked  string  `json:"last_checked"`
		}

		statuses := make([]MonitorStatus, len(monitors))
		for i, monitor := range monitors {
			monitor.mu.Lock()
			status := MonitorStatus{
				URL:          monitor.URL,
				Status:       monitor.Status,
				StatusCode:   monitor.LastStatusCode,
				ResponseTime: monitor.LastResponseTime.Seconds() * 1000, // Convert to milliseconds
				LastChecked:  monitor.LastChecked.Format(time.RFC3339),
			}
			statuses[i] = status
			monitor.mu.Unlock()
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(statuses)
	}
}

// Handler for POST /api/monitor/add
func addMonitorHandler(ctx context.Context, monitors []*Monitor, config *Config, limiter *rate.Limiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var data struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			http.Error(w, "Invalid request payload", http.StatusBadRequest)
			return
		}

		if data.URL == "" {
			http.Error(w, "URL is required", http.StatusBadRequest)
			return
		}

		// Check if monitor already exists
		for _, monitor := range monitors {
			if monitor.URL == data.URL {
				http.Error(w, "Monitor already exists", http.StatusConflict)
				return
			}
		}

		// Create and add new monitor
		newMonitor := NewMonitor(data.URL, config.MaxHistoryEntries, config.RetryAttempts, config.RetryDelay, &http.Client{Timeout: config.Timeout})
		monitors = append(monitors, newMonitor)

		// Start monitoring the new URL
		go newMonitor.Run(ctx, func(message string) {
			if err := limiter.Wait(ctx); err != nil {
				log.Printf("Rate limit error: %v", err)
				return
			}
			if err := alertFunc(ctx, message, config.WebhookURL); err != nil {
				log.Printf("Error sending alert: %v", err)
			}
		})

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(map[string]string{"message": "Site added successfully"})
	}
}

// Handler for GET /api/chart
func getChartDataHandler(monitors []*Monitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Access-Control-Allow-Origin", "*")

		type ChartData struct {
			Name     string  `json:"name"`
			Uptime   float64 `json:"uptime"`
			Downtime float64 `json:"downtime"`
		}

		chartData := make([]ChartData, len(monitors))
		for i, monitor := range monitors {
			monitor.mu.Lock()
			totalChecks := monitor.UptimeCount + monitor.DowntimeCount
			uptimePercentage := 0.0
			downtimePercentage := 0.0
			if totalChecks > 0 {
				uptimePercentage = (float64(monitor.UptimeCount) / float64(totalChecks)) * 100
				downtimePercentage = (float64(monitor.DowntimeCount) / float64(totalChecks)) * 100
			}
			chartData[i] = ChartData{
				Name:     monitor.URL,
				Uptime:   uptimePercentage,
				Downtime: downtimePercentage,
			}
			monitor.mu.Unlock()
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(chartData)
	}
}

// Handler for GET /api/monitor/history
func getMonitorHistoryHandler(monitors []*Monitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Access-Control-Allow-Origin", "*")

		url := r.URL.Query().Get("url")
		if url == "" {
			http.Error(w, "URL parameter is required", http.StatusBadRequest)
			return
		}

		var history []string
		for _, monitor := range monitors {
			if monitor.URL == url {
				history = monitor.GetHistory()
				break
			}
		}

		if history == nil {
			http.Error(w, "Monitor not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(history)
	}
}
