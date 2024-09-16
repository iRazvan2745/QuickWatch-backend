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
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

type Config struct {
	URLs            []string      `json:"urls"`
	CheckInterval   time.Duration `json:"check_interval"`
	WebhookURL      string        `json:"webhook_url"`
	LogFile         string        `json:"log_file"`
	RetryAttempts   int           `json:"retry_attempts"`
	RetryDelay      time.Duration `json:"retry_delay"`
	Timeout         time.Duration `json:"timeout"`
	AlertThreshold  int           `json:"alert_threshold"`
	RateLimit       rate.Limit    `json:"rate_limit"`
	RateBurst       int           `json:"rate_burst"`
	MetricsPort     string        `json:"metrics_port"`
	HealthCheckPort string        `json:"health_check_port"`
	TLSCertFile     string        `json:"tls_cert_file"`
	TLSKeyFile      string        `json:"tls_key_file"`
	APIPort         string        `json:"api_port"`
}

func main() {
	config, err := loadConfig()
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
	}

	if err := setupLogging(config.LogFile); err != nil {
		log.Fatalf("Error setting up logging: %v", err)
	}

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
		monitors[i] = NewMonitor(url, int(config.CheckInterval.Seconds()), config.AlertThreshold, config.RetryAttempts, config.RetryDelay, &http.Client{Timeout: config.Timeout})
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
		return startAPIServer(ctx, monitors, config.APIPort, limiter, config)
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

func alertFunc(ctx context.Context, message, webhookURL string) error {
	timestamp := time.Now().Format(time.RFC3339)
	log.Printf("%s %s", timestamp, message)

	return sendDiscordWebhook(ctx, message, webhookURL)
}

func sendDiscordWebhook(ctx context.Context, message, webhookURL string) error {
	if webhookURL == "" {
		return fmt.Errorf("Discord webhook URL not set")
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
	return &Config{
		URLs:            []string{"https://examplsfsdfsfgtse.com", "https://sgfg.com"},
		CheckInterval:   10 * time.Second,
		WebhookURL:      "https://discord.com/api/webhooks/1270238182935629947/l6rx-Yybi5SedNwa05B_p88njbeBM-UiQ1wEiR8cO3aq-EUae6DI2pfHEJYGm6K99suE",
		LogFile:         "monitoring.log",
		RetryAttempts:   3,
		RetryDelay:      5 * time.Second,
		Timeout:         30 * time.Second,
		AlertThreshold:  3,
		RateLimit:       rate.Limit(1),
		RateBurst:       5,
		HealthCheckPort: ":8081",
		TLSCertFile:     "server.crt",
		TLSKeyFile:      "server.key",
		APIPort:         ":8080",
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

func startAPIServer(ctx context.Context, monitors []*Monitor, addr string, limiter *rate.Limiter, config *Config) error {
	mux := http.NewServeMux()

	// ... existing /api/monitor handler ...

	mux.HandleFunc("/api/monitor/add", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var data struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Create a new monitor for the added URL
		newMonitor := NewMonitor(data.URL, int(config.CheckInterval.Seconds()), config.AlertThreshold, config.RetryAttempts, config.RetryDelay, &http.Client{Timeout: config.Timeout})
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
			log.Printf("Error shutting down API server: %v", err)
		}
	}()

	log.Printf("Starting API server on %s", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("API server error: %w", err)
	}

	return nil
}
