package collector

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/vinted/sonic-exporter/pkg/redis"
)

type dockerCollectorConfig struct {
	enabled              bool
	refreshInterval      time.Duration
	timeout              time.Duration
	maxContainers        int
	sourceStaleThreshold time.Duration
	redisScanCount       int64
}

type dockerCollector struct {
	containerInfo          *prometheus.Desc
	containerCPUPercent    *prometheus.Desc
	containerMemoryBytes   *prometheus.Desc
	containerMemoryLimit   *prometheus.Desc
	containerMemoryPercent *prometheus.Desc
	containerNetworkRx     *prometheus.Desc
	containerNetworkTx     *prometheus.Desc
	containerBlockRead     *prometheus.Desc
	containerBlockWrite    *prometheus.Desc
	containerPids          *prometheus.Desc
	containerCount         *prometheus.Desc
	sourceLastUpdate       *prometheus.Desc
	sourceAge              *prometheus.Desc
	sourceStale            *prometheus.Desc
	entriesSkipped         *prometheus.Desc
	scrapeDuration         *prometheus.Desc
	scrapeCollectorSuccess *prometheus.Desc
	cacheAge               *prometheus.Desc

	logger *slog.Logger
	config dockerCollectorConfig

	mu                   sync.RWMutex
	cachedMetrics        []prometheus.Metric
	lastSuccess          float64
	lastScrapeDuration   float64
	lastRefreshTime      time.Time
	lastSourceUpdateTime time.Time
	lastSourceStale      float64
	lastEntriesSkipped   float64
	lastContainerCount   float64
}

func NewDockerCollector(logger *slog.Logger) *dockerCollector {
	const (
		namespace = "sonic"
		subsystem = "docker"
	)

	collector := &dockerCollector{
		containerInfo: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "container_info"),
			"Container metadata from SONiC DOCKER_STATS, value is always 1", []string{"container"}, nil),
		containerCPUPercent: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "container_cpu_percent"),
			"Container CPU usage percent", []string{"container"}, nil),
		containerMemoryBytes: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "container_memory_usage_bytes"),
			"Container memory usage bytes", []string{"container"}, nil),
		containerMemoryLimit: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "container_memory_limit_bytes"),
			"Container memory limit bytes", []string{"container"}, nil),
		containerMemoryPercent: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "container_memory_percent"),
			"Container memory usage percent", []string{"container"}, nil),
		containerNetworkRx: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "container_network_receive_bytes_total"),
			"Container network receive bytes", []string{"container"}, nil),
		containerNetworkTx: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "container_network_transmit_bytes_total"),
			"Container network transmit bytes", []string{"container"}, nil),
		containerBlockRead: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "container_block_read_bytes_total"),
			"Container block read bytes", []string{"container"}, nil),
		containerBlockWrite: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "container_block_write_bytes_total"),
			"Container block write bytes", []string{"container"}, nil),
		containerPids: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "container_pids"),
			"Container process count", []string{"container"}, nil),
		containerCount: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "containers"),
			"Number of containers with DOCKER_STATS entries", nil, nil),
		sourceLastUpdate: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "source_last_update_timestamp_seconds"),
			"Unix timestamp of DOCKER_STATS|LastUpdateTime source update", nil, nil),
		sourceAge: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "source_age_seconds"),
			"Age in seconds of DOCKER_STATS source update", nil, nil),
		sourceStale: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "source_stale"),
			"Whether DOCKER_STATS source data is stale (1=yes, 0=no)", nil, nil),
		entriesSkipped: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries_skipped"),
			"Number of docker entries skipped during latest refresh", nil, nil),
		scrapeDuration: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "scrape_duration_seconds"),
			"Time it took for exporter to refresh docker metrics", nil, nil),
		scrapeCollectorSuccess: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "collector_success"),
			"Whether docker collector succeeded", nil, nil),
		cacheAge: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "cache_age_seconds"),
			"Age of latest docker cache refresh", nil, nil),
		logger: logger,
		config: loadDockerCollectorConfig(logger),
	}

	if !collector.config.enabled {
		collector.logger.Info("Docker collector is disabled")
		return collector
	}

	collector.refreshMetrics()
	go collector.refreshLoop()

	return collector
}

func (collector *dockerCollector) IsEnabled() bool {
	return collector.config.enabled
}

func (collector *dockerCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- collector.containerInfo
	ch <- collector.containerCPUPercent
	ch <- collector.containerMemoryBytes
	ch <- collector.containerMemoryLimit
	ch <- collector.containerMemoryPercent
	ch <- collector.containerNetworkRx
	ch <- collector.containerNetworkTx
	ch <- collector.containerBlockRead
	ch <- collector.containerBlockWrite
	ch <- collector.containerPids
	ch <- collector.containerCount
	ch <- collector.sourceLastUpdate
	ch <- collector.sourceAge
	ch <- collector.sourceStale
	ch <- collector.entriesSkipped
	ch <- collector.scrapeDuration
	ch <- collector.scrapeCollectorSuccess
	ch <- collector.cacheAge
}

func (collector *dockerCollector) Collect(ch chan<- prometheus.Metric) {
	if !collector.config.enabled {
		return
	}

	collector.mu.RLock()
	cachedMetrics := append([]prometheus.Metric{}, collector.cachedMetrics...)
	lastScrapeDuration := collector.lastScrapeDuration
	lastSuccess := collector.lastSuccess
	lastRefreshTime := collector.lastRefreshTime
	lastSourceUpdateTime := collector.lastSourceUpdateTime
	lastSourceStale := collector.lastSourceStale
	lastEntriesSkipped := collector.lastEntriesSkipped
	lastContainerCount := collector.lastContainerCount
	collector.mu.RUnlock()

	for _, metric := range cachedMetrics {
		ch <- metric
	}

	cacheAge := 0.0
	if !lastRefreshTime.IsZero() {
		cacheAge = time.Since(lastRefreshTime).Seconds()
	}

	sourceTimestamp := 0.0
	sourceAge := 0.0
	if !lastSourceUpdateTime.IsZero() {
		sourceTimestamp = float64(lastSourceUpdateTime.Unix())
		sourceAge = time.Since(lastSourceUpdateTime).Seconds()
		if sourceAge < 0 {
			sourceAge = 0
		}
	}

	ch <- prometheus.MustNewConstMetric(collector.containerCount, prometheus.GaugeValue, lastContainerCount)
	ch <- prometheus.MustNewConstMetric(collector.sourceLastUpdate, prometheus.GaugeValue, sourceTimestamp)
	ch <- prometheus.MustNewConstMetric(collector.sourceAge, prometheus.GaugeValue, sourceAge)
	ch <- prometheus.MustNewConstMetric(collector.sourceStale, prometheus.GaugeValue, lastSourceStale)
	ch <- prometheus.MustNewConstMetric(collector.entriesSkipped, prometheus.GaugeValue, lastEntriesSkipped)
	ch <- prometheus.MustNewConstMetric(collector.scrapeDuration, prometheus.GaugeValue, lastScrapeDuration)
	ch <- prometheus.MustNewConstMetric(collector.scrapeCollectorSuccess, prometheus.GaugeValue, lastSuccess)
	ch <- prometheus.MustNewConstMetric(collector.cacheAge, prometheus.GaugeValue, cacheAge)
}

func (collector *dockerCollector) refreshLoop() {
	ticker := time.NewTicker(collector.config.refreshInterval)
	defer ticker.Stop()

	for range ticker.C {
		collector.refreshMetrics()
	}
}

func (collector *dockerCollector) refreshMetrics() {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), collector.config.timeout)
	defer cancel()

	metrics, skippedEntries, containerCount, sourceUpdateTime, sourceStale, err := collector.scrapeMetrics(ctx)
	scrapeDuration := time.Since(start).Seconds()

	collector.mu.Lock()
	defer collector.mu.Unlock()

	collector.lastScrapeDuration = scrapeDuration

	if err != nil {
		collector.lastSuccess = 0
		collector.logger.Error("Error refreshing docker metrics", "error", err)
		return
	}

	collector.cachedMetrics = metrics
	collector.lastEntriesSkipped = float64(skippedEntries)
	collector.lastContainerCount = float64(containerCount)
	collector.lastSourceUpdateTime = sourceUpdateTime
	collector.lastSourceStale = sourceStale
	collector.lastSuccess = 1
	collector.lastRefreshTime = time.Now()
}

func (collector *dockerCollector) scrapeMetrics(ctx context.Context) ([]prometheus.Metric, int, int, time.Time, float64, error) {
	redisClient, err := redis.NewClient()
	if err != nil {
		return nil, 0, 0, time.Time{}, 1, fmt.Errorf("redis client initialization failed: %w", err)
	}
	defer redisClient.Close()

	sourceUpdateTime, sourceStale, err := collector.readLastUpdateTime(ctx, redisClient)
	if err != nil {
		return nil, 0, 0, time.Time{}, 1, err
	}

	dockerKeys, err := redisClient.ScanKeysFromDb(ctx, "STATE_DB", "DOCKER_STATS|*", collector.config.redisScanCount)
	if err != nil {
		return nil, 0, 0, sourceUpdateTime, sourceStale, fmt.Errorf("failed to scan DOCKER_STATS keys: %w", err)
	}

	sort.Strings(dockerKeys)
	metrics := make([]prometheus.Metric, 0, len(dockerKeys)*8)
	skippedEntries := 0
	containerCount := 0

	for _, dockerKey := range dockerKeys {
		if dockerKey == "DOCKER_STATS|LastUpdateTime" {
			continue
		}

		if containerCount >= collector.config.maxContainers {
			skippedEntries++
			continue
		}

		containerStats, err := redisClient.HgetAllFromDb(ctx, "STATE_DB", dockerKey)
		if err != nil {
			return nil, 0, 0, sourceUpdateTime, sourceStale, fmt.Errorf("failed to read %s: %w", dockerKey, err)
		}

		if len(containerStats) == 0 {
			skippedEntries++
			collector.logger.Debug("Docker entry has no fields", "key", dockerKey, "expected", true)
			continue
		}

		containerName := strings.TrimSpace(containerStats["NAME"])
		if containerName == "" {
			skippedEntries++
			collector.logger.Debug("Docker entry missing NAME field", "key", dockerKey, "expected", true)
			continue
		}

		metrics = append(metrics, prometheus.MustNewConstMetric(collector.containerInfo, prometheus.GaugeValue, 1, containerName))

		if cpuPercent, ok := parseDockerFloat(containerStats, "CPU%"); ok {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.containerCPUPercent, prometheus.GaugeValue, cpuPercent, containerName))
		} else {
			skippedEntries++
		}

		if memoryBytes, ok := parseDockerFloat(containerStats, "MEM_BYTES"); ok {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.containerMemoryBytes, prometheus.GaugeValue, memoryBytes, containerName))
		} else {
			skippedEntries++
		}

		if memoryLimit, ok := parseDockerFloat(containerStats, "MEM_LIMIT_BYTES"); ok {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.containerMemoryLimit, prometheus.GaugeValue, memoryLimit, containerName))
		} else {
			skippedEntries++
		}

		if memoryPercent, ok := parseDockerFloat(containerStats, "MEM%"); ok {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.containerMemoryPercent, prometheus.GaugeValue, memoryPercent, containerName))
		} else {
			skippedEntries++
		}

		if netIn, ok := parseDockerFloat(containerStats, "NET_IN_BYTES"); ok {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.containerNetworkRx, prometheus.CounterValue, netIn, containerName))
		} else {
			skippedEntries++
		}

		if netOut, ok := parseDockerFloat(containerStats, "NET_OUT_BYTES"); ok {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.containerNetworkTx, prometheus.CounterValue, netOut, containerName))
		} else {
			skippedEntries++
		}

		if blockIn, ok := parseDockerFloat(containerStats, "BLOCK_IN_BYTES"); ok {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.containerBlockRead, prometheus.CounterValue, blockIn, containerName))
		} else {
			skippedEntries++
		}

		if blockOut, ok := parseDockerFloat(containerStats, "BLOCK_OUT_BYTES"); ok {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.containerBlockWrite, prometheus.CounterValue, blockOut, containerName))
		} else {
			skippedEntries++
		}

		if pids, ok := parseDockerFloat(containerStats, "PIDS"); ok {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.containerPids, prometheus.GaugeValue, pids, containerName))
		} else {
			skippedEntries++
		}

		containerCount++
	}

	if containerCount == 0 {
		collector.logger.Debug("No DOCKER_STATS container entries found", "expected", true)
	}

	return metrics, skippedEntries, containerCount, sourceUpdateTime, sourceStale, nil
}

func (collector *dockerCollector) readLastUpdateTime(ctx context.Context, redisClient redis.Client) (time.Time, float64, error) {
	lastUpdateData, err := redisClient.HgetAllFromDb(ctx, "STATE_DB", "DOCKER_STATS|LastUpdateTime")
	if err != nil {
		return time.Time{}, 1, fmt.Errorf("failed to read DOCKER_STATS|LastUpdateTime: %w", err)
	}

	lastUpdateRaw := resolveDockerLastUpdateValue(lastUpdateData)
	if lastUpdateRaw == "" {
		collector.logger.Debug("DOCKER_STATS lastupdate field missing", "expected", true)
		return time.Time{}, 1, nil
	}

	parsedTime, err := parseDockerTimestamp(lastUpdateRaw)
	if err != nil {
		collector.logger.Debug("Failed to parse DOCKER_STATS lastupdate timestamp", "value", lastUpdateRaw, "error", err)
		return time.Time{}, 1, nil
	}

	age := time.Since(parsedTime)
	if age < 0 {
		age = 0
	}

	if age > collector.config.sourceStaleThreshold {
		collector.logger.Debug("DOCKER_STATS source is stale", "age_seconds", age.Seconds(), "threshold_seconds", collector.config.sourceStaleThreshold.Seconds())
		return parsedTime, 1, nil
	}

	return parsedTime, 0, nil
}

func resolveDockerLastUpdateValue(fields map[string]string) string {
	for _, key := range []string{"lastupdate", "last_update", "LastUpdate", "LastUpdateTime"} {
		if value := strings.TrimSpace(fields[key]); value != "" {
			return value
		}
	}

	for key, value := range fields {
		normalizedKey := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "_", ""))
		if normalizedKey == "lastupdate" || normalizedKey == "lastupdatetime" {
			trimmedValue := strings.TrimSpace(value)
			if trimmedValue != "" {
				return trimmedValue
			}
		}
	}

	return ""
}

func loadDockerCollectorConfig(logger *slog.Logger) dockerCollectorConfig {
	return dockerCollectorConfig{
		enabled:              parseBoolEnv(logger, "DOCKER_ENABLED", false),
		refreshInterval:      parseDurationEnv(logger, "DOCKER_REFRESH_INTERVAL", 60*time.Second),
		timeout:              parseDurationEnv(logger, "DOCKER_TIMEOUT", 2*time.Second),
		maxContainers:        parseIntEnv(logger, "DOCKER_MAX_CONTAINERS", 128),
		sourceStaleThreshold: parseDurationEnv(logger, "DOCKER_SOURCE_STALE_THRESHOLD", 5*time.Minute),
		redisScanCount:       256,
	}
}

func parseDockerFloat(fields map[string]string, key string) (float64, bool) {
	raw := strings.TrimSpace(fields[key])
	if raw == "" {
		return 0, false
	}

	parsedValue, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}

	return parsedValue, true
}

func parseDockerTimestamp(value string) (time.Time, error) {
	layouts := []string{
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05.999999Z07:00",
		time.RFC3339,
	}

	for _, layout := range layouts {
		if parsedTime, err := time.Parse(layout, value); err == nil {
			return parsedTime, nil
		}
	}

	return time.Time{}, fmt.Errorf("unsupported timestamp format: %s", value)
}
