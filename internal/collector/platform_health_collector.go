package collector

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/vinted/sonic-exporter/pkg/redis"
)

type platformHealthCollectorConfig struct {
	enabled           bool
	refreshInterval   time.Duration
	timeout           time.Duration
	maxProcesses      int
	maxStorageDevices int
	redisScanCount    int64
}

type platformHealthCollector struct {
	processInfo            *prometheus.Desc
	processCPUPercent      *prometheus.Desc
	processMemoryPercent   *prometheus.Desc
	processRunning         *prometheus.Desc
	storageInfo            *prometheus.Desc
	storageHealthPercent   *prometheus.Desc
	storageTemperature     *prometheus.Desc
	storageReservedBlocks  *prometheus.Desc
	storageFsioReads       *prometheus.Desc
	storageFsioWrites      *prometheus.Desc
	storageDiskReads       *prometheus.Desc
	storageDiskWrites      *prometheus.Desc
	systemHealthInfo       *prometheus.Desc
	entriesSkipped         *prometheus.Desc
	entriesTruncated       *prometheus.Desc
	scrapeDuration         *prometheus.Desc
	scrapeCollectorSuccess *prometheus.Desc
	cacheAge               *prometheus.Desc

	logger       *slog.Logger
	metricFilter MetricFilter
	config       platformHealthCollectorConfig

	mu                 sync.RWMutex
	cachedMetrics      []prometheus.Metric
	lastSuccess        float64
	lastScrapeDuration float64
	lastSkippedEntries float64
	lastTruncated      float64
	lastRefreshTime    time.Time
}

func NewPlatformHealthCollector(logger *slog.Logger, metricFilter MetricFilter) *platformHealthCollector {
	const (
		namespace = "sonic"
		subsystem = "platform"
	)

	collector := &platformHealthCollector{
		processInfo: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "process_info"),
			"Process metadata from PROCESS_STATS, value is always 1", []string{"pid", "process", "uid"}, nil),
		processCPUPercent: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "process_cpu_percent"),
			"Process CPU percent from PROCESS_STATS", []string{"pid", "process"}, nil),
		processMemoryPercent: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "process_memory_percent"),
			"Process memory percent from PROCESS_STATS", []string{"pid", "process"}, nil),
		processRunning: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "process_running"),
			"Whether process entry is present in PROCESS_STATS", []string{"pid", "process"}, nil),
		storageInfo: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "storage_info"),
			"Storage metadata from STORAGE_INFO, value is always 1", []string{"device", "model", "serial", "firmware"}, nil),
		storageHealthPercent: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "storage_health_percent"),
			"Storage health percent from STORAGE_INFO", []string{"device"}, nil),
		storageTemperature: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "storage_temperature_celsius"),
			"Storage temperature from STORAGE_INFO", []string{"device"}, nil),
		storageReservedBlocks: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "storage_reserved_blocks"),
			"Storage reserved blocks from STORAGE_INFO", []string{"device"}, nil),
		storageFsioReads: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "storage_total_fsio_reads_total"),
			"Storage total fsio reads from STORAGE_INFO", []string{"device"}, nil),
		storageFsioWrites: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "storage_total_fsio_writes_total"),
			"Storage total fsio writes from STORAGE_INFO", []string{"device"}, nil),
		storageDiskReads: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "storage_disk_io_reads_total"),
			"Storage disk I/O reads from STORAGE_INFO", []string{"device"}, nil),
		storageDiskWrites: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "storage_disk_io_writes_total"),
			"Storage disk I/O writes from STORAGE_INFO", []string{"device"}, nil),
		systemHealthInfo: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "system_health_info"),
			"System health summary from SYSTEM_HEALTH_INFO, value is always 1", []string{"summary"}, nil),
		entriesSkipped: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries_skipped"),
			"Number of platform health entries skipped during latest refresh", nil, nil),
		entriesTruncated: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries_truncated"),
			"Whether platform health collection hit entry limits (1=yes, 0=no)", nil, nil),
		scrapeDuration: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "scrape_duration_seconds"),
			"Time it took for exporter to refresh platform health metrics", nil, nil),
		scrapeCollectorSuccess: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "collector_success"),
			"Whether platform health collector succeeded", nil, nil),
		cacheAge: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "cache_age_seconds"),
			"Age of latest platform health cache refresh", nil, nil),
		logger:       logger,
		metricFilter: metricFilter,
		config: platformHealthCollectorConfig{
			enabled:           parseBoolEnv(logger, "PLATFORM_HEALTH_ENABLED", false),
			refreshInterval:   parseDurationEnv(logger, "PLATFORM_HEALTH_REFRESH_INTERVAL", 60*time.Second),
			timeout:           parseDurationEnv(logger, "PLATFORM_HEALTH_TIMEOUT", 2*time.Second),
			maxProcesses:      parseIntEnv(logger, "PLATFORM_HEALTH_MAX_PROCESSES", 512),
			maxStorageDevices: parseIntEnv(logger, "PLATFORM_HEALTH_MAX_STORAGE_DEVICES", 128),
			redisScanCount:    256,
		},
	}

	if !collector.config.enabled {
		collector.logger.Info("Platform health collector is disabled")
		return collector
	}

	collector.refreshMetrics()
	go collector.refreshLoop()

	return collector
}

func (collector *platformHealthCollector) IsEnabled() bool { return collector.config.enabled }

func (collector *platformHealthCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- collector.processInfo
	ch <- collector.processCPUPercent
	ch <- collector.processMemoryPercent
	ch <- collector.processRunning
	ch <- collector.storageInfo
	ch <- collector.storageHealthPercent
	ch <- collector.storageTemperature
	ch <- collector.storageReservedBlocks
	ch <- collector.storageFsioReads
	ch <- collector.storageFsioWrites
	ch <- collector.storageDiskReads
	ch <- collector.storageDiskWrites
	ch <- collector.systemHealthInfo
	ch <- collector.entriesSkipped
	ch <- collector.entriesTruncated
	ch <- collector.scrapeDuration
	ch <- collector.scrapeCollectorSuccess
	ch <- collector.cacheAge
}

func (collector *platformHealthCollector) Collect(ch chan<- prometheus.Metric) {
	if !collector.config.enabled {
		return
	}

	collector.mu.RLock()
	cachedMetrics := append([]prometheus.Metric{}, collector.cachedMetrics...)
	lastScrapeDuration := collector.lastScrapeDuration
	lastSuccess := collector.lastSuccess
	lastSkippedEntries := collector.lastSkippedEntries
	lastTruncated := collector.lastTruncated
	lastRefreshTime := collector.lastRefreshTime
	collector.mu.RUnlock()

	for _, metric := range cachedMetrics {
		ch <- metric
	}

	cacheAge := 0.0
	if !lastRefreshTime.IsZero() {
		cacheAge = time.Since(lastRefreshTime).Seconds()
	}
	if collector.metricFilter.Enabled("sonic_platform_entries_skipped") {
		ch <- prometheus.MustNewConstMetric(collector.entriesSkipped, prometheus.GaugeValue, lastSkippedEntries)
	}
	if collector.metricFilter.Enabled("sonic_platform_entries_truncated") {
		ch <- prometheus.MustNewConstMetric(collector.entriesTruncated, prometheus.GaugeValue, lastTruncated)
	}
	if collector.metricFilter.Enabled("sonic_platform_scrape_duration_seconds") {
		ch <- prometheus.MustNewConstMetric(collector.scrapeDuration, prometheus.GaugeValue, lastScrapeDuration)
	}
	if collector.metricFilter.Enabled("sonic_platform_collector_success") {
		ch <- prometheus.MustNewConstMetric(collector.scrapeCollectorSuccess, prometheus.GaugeValue, lastSuccess)
	}
	if collector.metricFilter.Enabled("sonic_platform_cache_age_seconds") {
		ch <- prometheus.MustNewConstMetric(collector.cacheAge, prometheus.GaugeValue, cacheAge)
	}
}

func (collector *platformHealthCollector) refreshLoop() {
	ticker := time.NewTicker(collector.config.refreshInterval)
	defer ticker.Stop()
	for range ticker.C {
		collector.refreshMetrics()
	}
}

func (collector *platformHealthCollector) refreshMetrics() {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), collector.config.timeout)
	defer cancel()
	metrics, skippedEntries, truncated, err := collector.scrapeMetrics(ctx)
	scrapeDuration := time.Since(start).Seconds()

	collector.mu.Lock()
	defer collector.mu.Unlock()
	collector.lastScrapeDuration = scrapeDuration
	if err != nil {
		collector.lastSuccess = 0
		collector.logger.Error("Error refreshing platform health metrics", "error", err)
		return
	}
	collector.cachedMetrics = metrics
	collector.lastSkippedEntries = float64(skippedEntries)
	collector.lastTruncated = truncated
	collector.lastSuccess = 1
	collector.lastRefreshTime = time.Now()
}

func (collector *platformHealthCollector) scrapeMetrics(ctx context.Context) ([]prometheus.Metric, int, float64, error) {
	redisClient, err := redis.NewClient()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("redis client initialization failed: %w", err)
	}
	defer redisClient.Close()

	metrics := []prometheus.Metric{}
	skippedEntries := 0
	truncated := 0.0

	processKeys, err := redisClient.ScanKeysFromDb(ctx, "STATE_DB", "PROCESS_STATS|*", collector.config.redisScanCount)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to scan process keys: %w", err)
	}
	sort.Strings(processKeys)
	processCount := 0
	for _, processKey := range processKeys {
		if processKey == "PROCESS_STATS|LastUpdateTime" {
			continue
		}
		if processCount >= collector.config.maxProcesses {
			truncated = 1
			skippedEntries++
			continue
		}

		pid, err := parseKeySuffix(processKey, "PROCESS_STATS|")
		if err != nil {
			skippedEntries++
			continue
		}

		processData, err := redisClient.HgetAllFromDb(ctx, "STATE_DB", processKey)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("failed to read process entry %s: %w", processKey, err)
		}
		if len(processData) == 0 {
			skippedEntries++
			continue
		}

		processName := normalizeProcessName(processData["CMD"])
		uid := strings.TrimSpace(processData["UID"])
		if collector.metricFilter.Enabled("sonic_platform_process_info") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.processInfo, prometheus.GaugeValue, 1, pid, processName, uid))
		}
		if collector.metricFilter.Enabled("sonic_platform_process_running") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.processRunning, prometheus.GaugeValue, 1, pid, processName))
		}
		if value, ok := parseCounterLike(processData["CPU"]); ok && collector.metricFilter.Enabled("sonic_platform_process_cpu_percent") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.processCPUPercent, prometheus.GaugeValue, value, pid, processName))
		}
		if value, ok := parseCounterLike(processData["MEM"]); ok && collector.metricFilter.Enabled("sonic_platform_process_memory_percent") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.processMemoryPercent, prometheus.GaugeValue, value, pid, processName))
		}

		processCount++
	}

	storageKeys, err := redisClient.ScanKeysFromDb(ctx, "STATE_DB", "STORAGE_INFO|*", collector.config.redisScanCount)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to scan storage keys: %w", err)
	}
	sort.Strings(storageKeys)
	storageCount := 0
	for _, storageKey := range storageKeys {
		device, err := parseKeySuffix(storageKey, "STORAGE_INFO|")
		if err != nil || device == "FSSTATS_SYNC" {
			continue
		}
		if storageCount >= collector.config.maxStorageDevices {
			truncated = 1
			skippedEntries++
			continue
		}

		storageData, err := redisClient.HgetAllFromDb(ctx, "STATE_DB", storageKey)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("failed to read storage entry %s: %w", storageKey, err)
		}
		if len(storageData) == 0 {
			skippedEntries++
			continue
		}

		if collector.metricFilter.Enabled("sonic_platform_storage_info") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.storageInfo, prometheus.GaugeValue, 1, device, storageData["device_model"], storageData["serial"], storageData["firmware"]))
		}
		if value, ok := parseCounterLike(storageData["health"]); ok && collector.metricFilter.Enabled("sonic_platform_storage_health_percent") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.storageHealthPercent, prometheus.GaugeValue, value, device))
		}
		if value, ok := parseCounterLike(storageData["temperature"]); ok && collector.metricFilter.Enabled("sonic_platform_storage_temperature_celsius") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.storageTemperature, prometheus.GaugeValue, value, device))
		}
		if value, ok := parseCounterLike(storageData["reserved_blocks"]); ok && collector.metricFilter.Enabled("sonic_platform_storage_reserved_blocks") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.storageReservedBlocks, prometheus.GaugeValue, value, device))
		}
		if value, ok := parseCounterLike(storageData["total_fsio_reads"]); ok && collector.metricFilter.Enabled("sonic_platform_storage_total_fsio_reads_total") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.storageFsioReads, prometheus.CounterValue, value, device))
		}
		if value, ok := parseCounterLike(storageData["total_fsio_writes"]); ok && collector.metricFilter.Enabled("sonic_platform_storage_total_fsio_writes_total") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.storageFsioWrites, prometheus.CounterValue, value, device))
		}
		if value, ok := parseCounterLike(storageData["disk_io_reads"]); ok && collector.metricFilter.Enabled("sonic_platform_storage_disk_io_reads_total") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.storageDiskReads, prometheus.CounterValue, value, device))
		}
		if value, ok := parseCounterLike(storageData["disk_io_writes"]); ok && collector.metricFilter.Enabled("sonic_platform_storage_disk_io_writes_total") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.storageDiskWrites, prometheus.CounterValue, value, device))
		}

		storageCount++
	}

	systemHealthData, err := redisClient.HgetAllFromDb(ctx, "STATE_DB", "SYSTEM_HEALTH_INFO")
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to read SYSTEM_HEALTH_INFO: %w", err)
	}
	if len(systemHealthData) > 0 {
		fields := make([]string, 0, len(systemHealthData))
		for field := range systemHealthData {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		for _, field := range fields {
			if collector.metricFilter.Enabled("sonic_platform_system_health_info") {
				summary := strings.TrimSpace(field + "=" + systemHealthData[field])
				metrics = append(metrics, prometheus.MustNewConstMetric(collector.systemHealthInfo, prometheus.GaugeValue, 1, summary))
			}
		}
	}

	return metrics, skippedEntries, truncated, nil
}
