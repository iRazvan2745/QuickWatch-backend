# QuickWatch-backend

## Description
QuickWatch-backend is a robust URL monitoring service that checks the availability of specified websites and provides real-time status updates.

## Prerequisites
- Go 1.16 or higher
- Docker (optional, for containerized deployment)

## Installation

1. Clone the repository:
   ```
   git clone https://github.com/yourusername/QuickWatch-backend.git
   cd QuickWatch-backend
   ```

2. Install dependencies:
   ```
   go mod tidy
   ```

## Configuration

1. Create a `.env` file in the root directory with the following variables:
   ```
   PORT=8080
   HEALTH_CHECK_PORT=8081
   DISCORD_WEBHOOK_URL=your_discord_webhook_url
   URLS_TO_MONITOR=https://example.com,https://another-example.com
   ```

2. Adjust the values as needed.

## Running the Application

1. Start the application:
   ```
   go run main.go
   ```

2. The application will start monitoring the specified URLs and:
   - Run the main API server on port 8080
   - Run the health check server on port 8081
   - Log monitoring results to `monitoring.log`

## Docker Deployment (Optional)

1. Build the Docker image:
   ```
   docker build -t quickwatch-backend .
   ```

2. Run the container:
   ```
   docker run -p 8080:8080 -p 8081:8081 --env-file .env quickwatch-backend
   ```

## API Endpoints

- `GET /api/monitor`: Returns the current status of all monitored URLs
- `POST /api/monitor/add`: Adds a new URL to be monitored
- `GET /health`: Health check endpoint

For more detailed API documentation, please refer to the API specification document.