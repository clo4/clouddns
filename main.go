package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DNSRecord represents a DNS record to update
type DNSRecord struct {
	// Name is the "host" name for the record, fully qualified.
	Name string `json:"name"`
	// APIToken is the token used to make the request to the Cloudflare API.
	// Specifying this per-record allows for different tokens to be used for different records.
	APIToken string `json:"api_token"`
	// ZoneID is the "zone ID", which is the ID for the configuration for a given domain name.
	ZoneID string `json:"zone_id"`
	// RecordID is the ID for the DNS record to update. This is only exposed through the API.
	RecordID string `json:"record_id"`
}

// DNSConfiguration holds separate lists of A and AAAA records
type DNSConfiguration struct {
	A    []DNSRecord `json:"a,omitempty"`
	AAAA []DNSRecord `json:"aaaa,omitempty"`
}

func loadDNSConfiguration() (DNSConfiguration, error) {
	var configuration DNSConfiguration

	configPath := os.Getenv("DDNS_CONFIG_PATH")
	if configPath == "" {
		return configuration, fmt.Errorf("DDNS_CONFIG_PATH environment variable not set")
	}

	configFile, err := os.ReadFile(configPath)
	if err != nil {
		return configuration, fmt.Errorf("failed to read config file: %w", err)
	}

	err = json.Unmarshal(configFile, &configuration)
	if err != nil {
		return configuration, fmt.Errorf("failed to parse config file: %w", err)
	}

	if len(configuration.A) == 0 && len(configuration.AAAA) == 0 {
		return configuration, fmt.Errorf("no DNS records found in config file")
	}

	return configuration, nil
}

func getCachePath() string {
	return os.Getenv("DDNS_CACHE_PATH")
}

// sanitizeString keeps latin alphanumerics and hyphens, and replaces
// every other character with an underscore.
func sanitizeString(input string) string {
	var sb strings.Builder
	sb.Grow(len(input))

	lastWasUnderscore := false

	for _, r := range input {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' {
			sb.WriteRune(r)
			lastWasUnderscore = false
		} else {
			// This could be combined into an if/else-if/else, but the control flow isn't
			// as clear, so it's better to keep it nested in the else block.
			if lastWasUnderscore {
				continue
			}
			sb.WriteRune('_')
			lastWasUnderscore = true
		}
	}

	return sb.String()
}

func generateCacheFilename(record *DNSRecord, recordType string) string {
	safeName := sanitizeString(record.Name)
	safeKey := recordType + "_" + safeName + "_" + record.RecordID
	return "cached_ip_" + safeKey + ".txt"
}

// CloudflareUpdateRequest represents the Cloudflare API request
type CloudflareUpdateRequest struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
}

// CloudflareResponse represents the API response structure
type CloudflareResponse struct {
	Success bool              `json:"success"`
	Errors  []CloudflareError `json:"errors,omitempty"`
}

// CloudflareError represents an error in the API response
type CloudflareError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func updateCloudflareRecord(client *http.Client, record *DNSRecord, recordType string, address string) error {
	url := "https://api.cloudflare.com/client/v4/zones/" + record.ZoneID + "/dns_records/" + record.RecordID

	updateReq := CloudflareUpdateRequest{
		Type:    recordType,
		Name:    record.Name,
		Content: address,
		TTL:     1,
	}

	jsonData, err := json.Marshal(updateReq)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	req, err := http.NewRequest("PUT", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+record.APIToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		var cfResp CloudflareResponse
		if err := json.Unmarshal(body, &cfResp); err == nil && len(cfResp.Errors) > 0 {
			return fmt.Errorf("API error: %s (code: %d)", cfResp.Errors[0].Message, cfResp.Errors[0].Code)
		}
		return fmt.Errorf("API error: %d %s", resp.StatusCode, string(body))
	}

	return nil
}

func readCachedIP(basePath, fileName string) (string, error) {
	if basePath == "" {
		// Reading from a non-existent cache is not an error, it should
		// return nothing because there was nothing to read.
		return "", nil
	}

	cachePath := filepath.Join(basePath, fileName)
	data, err := os.ReadFile(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // File doesn't exist yet, not an error
		}
		return "", fmt.Errorf("failed to read cache file: %w", err)
	}

	return strings.TrimSpace(string(data)), nil
}

func writeCachedIP(basePath, fileName, content string) error {
	// Writing to a non-existent cache is an error.
	if basePath == "" {
		return fmt.Errorf("cannot write cache file, no base path provided")
	}

	cachePath := filepath.Join(basePath, fileName)
	err := os.WriteFile(cachePath, []byte(content), 0644)
	if err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	return nil
}

func getCurrentIP(client *http.Client, api string) (string, error) {
	resp, err := client.Get(api)
	if err != nil {
		return "", fmt.Errorf("failed to request IP: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("IP service returned status code %d", resp.StatusCode)
	}

	ipBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read IP response: %w", err)
	}

	return strings.TrimSpace(string(ipBytes)), nil
}

// syncRecord ensures that the DNS record is up-to-date with the current IP address.
// If the cached IP matches the current IP, skip update for this record.
func syncRecord(
	logger *slog.Logger,
	client *http.Client,
	record *DNSRecord,
	recordType string,
	baseCachePath string,
	currentIP string,
) {
	cacheFileName := generateCacheFilename(record, recordType)
	cachedIP, err := readCachedIP(baseCachePath, cacheFileName)
	if err != nil {
		logger.Warn("Failed to read cached IP for record",
			"name", record.Name,
			"record_id", record.RecordID,
			"error", err)
		// Continue as if the cached IP is ""
	}

	// If cached IP address matches current IP address, skip update for this record
	if cachedIP == currentIP {
		logger.Info("IP address unchanged for record, skipping update",
			"name", record.Name,
			"record_id", record.RecordID,
			"ip", currentIP)
		return
	}

	logger.Info("Updating DNS record",
		"name", record.Name,
		"record_id", record.RecordID,
		"old_ip", cachedIP,
		"new_ip", currentIP,
		"record_type", recordType)

	err = updateCloudflareRecord(
		client,
		record,
		recordType,
		currentIP)

	if err != nil {
		logger.Error("Failed to update DNS record",
			"name", record.Name,
			"record_id", record.RecordID,
			"error", err)
	} else {
		logger.Info("Successfully updated DNS record",
			"name", record.Name,
			"record_id", record.RecordID,
			"ip", currentIP)

		// Only cache IP for this record if the update was successful
		if baseCachePath != "" {
			err = writeCachedIP(baseCachePath, cacheFileName, currentIP)
			if err != nil {
				logger.Warn("Failed to save cached IP for record",
					"name", record.Name,
					"record_id", record.RecordID,
					"error", err)
			} else {
				logger.Info("Successfully cached new IP address for record",
					"name", record.Name,
					"record_id", record.RecordID,
					"ip", currentIP)
			}
		} else {
			logger.Info("Not caching IP address because there is no DDNS_CACHE_PATH set",
				"name", record.Name,
				"record_id", record.RecordID,
				"ip", currentIP)
		}
	}
}

type DNSUpdateConfig struct {
	// logger is the structured logger to use for logging.
	logger *slog.Logger
	// client is the HTTP client to use for making requests.
	client *http.Client
	// records is a slice of DNSRecord structs representing the DNS records to update.
	// All records in this slice will be updated using this configuration.
	records []DNSRecord
	// recordType is the "type" field in the Cloudflare DNS update API request.
	// This is expected to be "A" or "AAAA".
	recordType string
	// baseCachePath is the directory where cache files are stored.
	// If this is an empty string, cache files will not be used,
	// which means that the DNS records will be updated every time, even
	// if the IP address has not changed from the last run.
	baseCachePath string
	// ipAPIURL is the URL to use for fetching the current IP address.
	// It is expected to return a plain string containing only an IP address.
	// It does not matter which form of address it returns.
	ipAPIURL string
}

func syncRecordsToIPAddress(config DNSUpdateConfig) {
	currentIP, err := getCurrentIP(config.client, config.ipAPIURL)
	if err != nil {
		config.logger.Error("Failed to get current IP address", "error", err)
		return
	}

	var wg sync.WaitGroup

	for i := range config.records {
		wg.Add(1)
		go func() {
			defer wg.Done()
			syncRecord(
				config.logger,
				config.client,
				&config.records[i],
				config.recordType,
				config.baseCachePath,
				currentIP,
			)
		}()
	}

	wg.Wait()
}

func run(logger *slog.Logger) error {
	logger.Info("Starting DDNS client")

	baseCachePath := getCachePath()
	logger.Info("Cache path", "path", baseCachePath)

	configuration, err := loadDNSConfiguration()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}
	logger.Info("Loaded configuration")

	client := &http.Client{Timeout: 10 * time.Second}

	var wg sync.WaitGroup

	a_records := len(configuration.A)
	if a_records > 0 {
		logger.Info("Updating A records", "count", a_records)
		wg.Add(1)
		go func() {
			defer wg.Done()
			syncRecordsToIPAddress(DNSUpdateConfig{
				logger:        logger.With("record_type", "A"),
				client:        client,
				records:       configuration.A,
				recordType:    "A",
				baseCachePath: baseCachePath,
				ipAPIURL:      "https://api.ipify.org",
			})
		}()
	}

	aaaa_records := len(configuration.AAAA)
	if aaaa_records > 0 {
		logger.Info("Updating AAAA records", "count", aaaa_records)
		wg.Add(1)
		go func() {
			defer wg.Done()
			syncRecordsToIPAddress(DNSUpdateConfig{
				logger:        logger.With("record_type", "AAAA"),
				client:        client,
				records:       configuration.AAAA,
				recordType:    "AAAA",
				baseCachePath: baseCachePath,
				ipAPIURL:      "https://api6.ipify.org",
			})
		}()
	}

	wg.Wait()

	logger.Info("DDNS client finished")
	return nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	if err := run(logger); err != nil {
		logger.Error("Application failed", "error", err)
		os.Exit(1)
	}
}
