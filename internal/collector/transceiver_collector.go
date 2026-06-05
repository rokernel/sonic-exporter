package collector

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/vinted/sonic-exporter/pkg/redis"
)

type transceiverCollectorConfig struct {
	enabled         bool
	refreshInterval time.Duration
	timeout         time.Duration
	maxPorts        int
	redisScanCount  int64
}

type transceiverCollector struct {
	moduleInfo             *prometheus.Desc
	statusValue            *prometheus.Desc
	statusFlagValue        *prometheus.Desc
	statusFlagChanges      *prometheus.Desc
	statusFlagLastSet      *prometheus.Desc
	statusFlagLastClear    *prometheus.Desc
	domFlagValue           *prometheus.Desc
	domFlagChanges         *prometheus.Desc
	domFlagLastSet         *prometheus.Desc
	domFlagLastClear       *prometheus.Desc
	domThresholdValue      *prometheus.Desc
	entriesSkipped         *prometheus.Desc
	entriesTruncated       *prometheus.Desc
	scrapeDuration         *prometheus.Desc
	scrapeCollectorSuccess *prometheus.Desc
	cacheAge               *prometheus.Desc

	logger       *slog.Logger
	metricFilter MetricFilter
	config       transceiverCollectorConfig

	mu                 sync.RWMutex
	cachedMetrics      []prometheus.Metric
	lastSuccess        float64
	lastScrapeDuration float64
	lastSkippedEntries float64
	lastTruncated      float64
	lastRefreshTime    time.Time
}

func NewTransceiverCollector(logger *slog.Logger, metricFilter MetricFilter) *transceiverCollector {
	const (
		namespace = "sonic"
		subsystem = "transceiver"
	)

	collector := &transceiverCollector{
		moduleInfo: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "module_info"),
			"Transceiver module state metadata, value is always 1", []string{"device", "module_state", "module_fault_cause"}, nil),
		statusValue: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "status_value"),
			"Transceiver status values from STATE_DB TRANSCEIVER_STATUS", []string{"device", "field"}, nil),
		statusFlagValue: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "status_flag_value"),
			"Transceiver status flag value from STATE_DB TRANSCEIVER_STATUS_FLAG", []string{"device", "flag"}, nil),
		statusFlagChanges: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "status_flag_changes_total"),
			"Transceiver status flag change count", []string{"device", "flag"}, nil),
		statusFlagLastSet: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "status_flag_last_set_timestamp_seconds"),
			"Unix timestamp when a transceiver status flag was last set", []string{"device", "flag"}, nil),
		statusFlagLastClear: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "status_flag_last_clear_timestamp_seconds"),
			"Unix timestamp when a transceiver status flag was last cleared", []string{"device", "flag"}, nil),
		domFlagValue: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "dom_flag_value"),
			"Transceiver DOM flag value from STATE_DB TRANSCEIVER_DOM_FLAG", []string{"device", "flag"}, nil),
		domFlagChanges: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "dom_flag_changes_total"),
			"Transceiver DOM flag change count", []string{"device", "flag"}, nil),
		domFlagLastSet: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "dom_flag_last_set_timestamp_seconds"),
			"Unix timestamp when a transceiver DOM flag was last set", []string{"device", "flag"}, nil),
		domFlagLastClear: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "dom_flag_last_clear_timestamp_seconds"),
			"Unix timestamp when a transceiver DOM flag was last cleared", []string{"device", "flag"}, nil),
		domThresholdValue: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "dom_threshold_value"),
			"Transceiver DOM threshold values", []string{"device", "threshold"}, nil),
		entriesSkipped: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries_skipped"),
			"Number of transceiver entries skipped during latest refresh", nil, nil),
		entriesTruncated: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries_truncated"),
			"Whether transceiver collection hit port limits (1=yes, 0=no)", nil, nil),
		scrapeDuration: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "scrape_duration_seconds"),
			"Time it took for exporter to refresh transceiver metrics", nil, nil),
		scrapeCollectorSuccess: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "collector_success"),
			"Whether transceiver collector succeeded", nil, nil),
		cacheAge: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "cache_age_seconds"),
			"Age of latest transceiver cache refresh", nil, nil),
		logger:       logger,
		metricFilter: metricFilter,
		config: transceiverCollectorConfig{
			enabled:         parseBoolEnv(logger, "TRANSCEIVER_ENABLED", true),
			refreshInterval: parseDurationEnv(logger, "TRANSCEIVER_REFRESH_INTERVAL", 60*time.Second),
			timeout:         parseDurationEnv(logger, "TRANSCEIVER_TIMEOUT", 2*time.Second),
			maxPorts:        parseIntEnv(logger, "TRANSCEIVER_MAX_PORTS", 1024),
			redisScanCount:  128,
		},
	}

	if !collector.config.enabled {
		collector.logger.Info("Transceiver collector is disabled")
		return collector
	}

	collector.refreshMetrics()
	go collector.refreshLoop()

	return collector
}

func (collector *transceiverCollector) IsEnabled() bool { return collector.config.enabled }

func (collector *transceiverCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- collector.moduleInfo
	ch <- collector.statusValue
	ch <- collector.statusFlagValue
	ch <- collector.statusFlagChanges
	ch <- collector.statusFlagLastSet
	ch <- collector.statusFlagLastClear
	ch <- collector.domFlagValue
	ch <- collector.domFlagChanges
	ch <- collector.domFlagLastSet
	ch <- collector.domFlagLastClear
	ch <- collector.domThresholdValue
	ch <- collector.entriesSkipped
	ch <- collector.entriesTruncated
	ch <- collector.scrapeDuration
	ch <- collector.scrapeCollectorSuccess
	ch <- collector.cacheAge
}

func (collector *transceiverCollector) Collect(ch chan<- prometheus.Metric) {
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
	if collector.metricFilter.Enabled("sonic_transceiver_entries_skipped") {
		ch <- prometheus.MustNewConstMetric(collector.entriesSkipped, prometheus.GaugeValue, lastSkippedEntries)
	}
	if collector.metricFilter.Enabled("sonic_transceiver_entries_truncated") {
		ch <- prometheus.MustNewConstMetric(collector.entriesTruncated, prometheus.GaugeValue, lastTruncated)
	}
	if collector.metricFilter.Enabled("sonic_transceiver_scrape_duration_seconds") {
		ch <- prometheus.MustNewConstMetric(collector.scrapeDuration, prometheus.GaugeValue, lastScrapeDuration)
	}
	if collector.metricFilter.Enabled("sonic_transceiver_collector_success") {
		ch <- prometheus.MustNewConstMetric(collector.scrapeCollectorSuccess, prometheus.GaugeValue, lastSuccess)
	}
	if collector.metricFilter.Enabled("sonic_transceiver_cache_age_seconds") {
		ch <- prometheus.MustNewConstMetric(collector.cacheAge, prometheus.GaugeValue, cacheAge)
	}
}

func (collector *transceiverCollector) refreshLoop() {
	ticker := time.NewTicker(collector.config.refreshInterval)
	defer ticker.Stop()
	for range ticker.C {
		collector.refreshMetrics()
	}
}

func (collector *transceiverCollector) refreshMetrics() {
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
		collector.logger.Error("Error refreshing transceiver metrics", "error", err)
		return
	}
	collector.cachedMetrics = metrics
	collector.lastSkippedEntries = float64(skippedEntries)
	collector.lastTruncated = truncated
	collector.lastSuccess = 1
	collector.lastRefreshTime = time.Now()
}

func (collector *transceiverCollector) scrapeMetrics(ctx context.Context) ([]prometheus.Metric, int, float64, error) {
	redisClient, err := redis.NewClient()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("redis client initialization failed: %w", err)
	}
	defer redisClient.Close()

	statusKeys, err := redisClient.ScanKeysFromDb(ctx, "STATE_DB", "TRANSCEIVER_STATUS|*", collector.config.redisScanCount)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to scan transceiver status keys: %w", err)
	}
	sort.Strings(statusKeys)

	metrics := []prometheus.Metric{}
	skippedEntries := 0
	truncated := 0.0

	for index, statusKey := range statusKeys {
		if index >= collector.config.maxPorts {
			truncated = 1
			skippedEntries += len(statusKeys) - index
			break
		}

		device, err := parseKeySuffix(statusKey, "TRANSCEIVER_STATUS|")
		if err != nil {
			skippedEntries++
			continue
		}

		statusData, err := redisClient.HgetAllFromDb(ctx, "STATE_DB", statusKey)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("failed to read transceiver status entry %s: %w", statusKey, err)
		}
		if len(statusData) == 0 {
			skippedEntries++
			continue
		}

		if collector.metricFilter.Enabled("sonic_transceiver_module_info") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.moduleInfo, prometheus.GaugeValue, 1, device, statusData["module_state"], statusData["module_fault_cause"]))
		}

		statusFields := make([]string, 0, len(statusData))
		for field := range statusData {
			statusFields = append(statusFields, field)
		}
		sort.Strings(statusFields)
		for _, field := range statusFields {
			if field == "module_state" || field == "module_fault_cause" || field == "last_update_time" {
				continue
			}

			if value, ok := parseBoolish(statusData[field]); ok && collector.metricFilter.Enabled("sonic_transceiver_status_value") {
				metrics = append(metrics, prometheus.MustNewConstMetric(collector.statusValue, prometheus.GaugeValue, value, device, field))
			}
		}

		domFlagData, err := redisClient.HgetAllFromDb(ctx, "STATE_DB", "TRANSCEIVER_DOM_FLAG|"+device)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("failed to read transceiver DOM flag entry for %s: %w", device, err)
		}
		domFlagChangesData, err := redisClient.HgetAllFromDb(ctx, "STATE_DB", "TRANSCEIVER_DOM_FLAG_CHANGE_COUNT|"+device)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("failed to read transceiver DOM flag change-count entry for %s: %w", device, err)
		}
		domFlagSetData, err := redisClient.HgetAllFromDb(ctx, "STATE_DB", "TRANSCEIVER_DOM_FLAG_SET_TIME|"+device)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("failed to read transceiver DOM flag set-time entry for %s: %w", device, err)
		}
		domFlagClearData, err := redisClient.HgetAllFromDb(ctx, "STATE_DB", "TRANSCEIVER_DOM_FLAG_CLEAR_TIME|"+device)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("failed to read transceiver DOM flag clear-time entry for %s: %w", device, err)
		}
		collector.appendTransceiverFlags(metrics, &metrics, collector.domFlagValue, collector.domFlagChanges, collector.domFlagLastSet, collector.domFlagLastClear, device, domFlagData,
			domFlagChangesData,
			domFlagSetData,
			domFlagClearData,
			collector.metricFilter.Enabled("sonic_transceiver_dom_flag_value"),
			collector.metricFilter.Enabled("sonic_transceiver_dom_flag_changes_total"),
			collector.metricFilter.Enabled("sonic_transceiver_dom_flag_last_set_timestamp_seconds"),
			collector.metricFilter.Enabled("sonic_transceiver_dom_flag_last_clear_timestamp_seconds"),
		)

		statusFlagData, err := redisClient.HgetAllFromDb(ctx, "STATE_DB", "TRANSCEIVER_STATUS_FLAG|"+device)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("failed to read transceiver status flag entry for %s: %w", device, err)
		}
		statusFlagChangesData, err := redisClient.HgetAllFromDb(ctx, "STATE_DB", "TRANSCEIVER_STATUS_FLAG_CHANGE_COUNT|"+device)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("failed to read transceiver status flag change-count entry for %s: %w", device, err)
		}
		statusFlagSetData, err := redisClient.HgetAllFromDb(ctx, "STATE_DB", "TRANSCEIVER_STATUS_FLAG_SET_TIME|"+device)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("failed to read transceiver status flag set-time entry for %s: %w", device, err)
		}
		statusFlagClearData, err := redisClient.HgetAllFromDb(ctx, "STATE_DB", "TRANSCEIVER_STATUS_FLAG_CLEAR_TIME|"+device)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("failed to read transceiver status flag clear-time entry for %s: %w", device, err)
		}
		collector.appendTransceiverFlags(metrics, &metrics, collector.statusFlagValue, collector.statusFlagChanges, collector.statusFlagLastSet, collector.statusFlagLastClear, device, statusFlagData,
			statusFlagChangesData,
			statusFlagSetData,
			statusFlagClearData,
			collector.metricFilter.Enabled("sonic_transceiver_status_flag_value"),
			collector.metricFilter.Enabled("sonic_transceiver_status_flag_changes_total"),
			collector.metricFilter.Enabled("sonic_transceiver_status_flag_last_set_timestamp_seconds"),
			collector.metricFilter.Enabled("sonic_transceiver_status_flag_last_clear_timestamp_seconds"),
		)

		thresholdData, err := redisClient.HgetAllFromDb(ctx, "STATE_DB", "TRANSCEIVER_DOM_THRESHOLD|"+device)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("failed to read transceiver threshold entry for %s: %w", device, err)
		}
		thresholdFields := make([]string, 0, len(thresholdData))
		for field := range thresholdData {
			thresholdFields = append(thresholdFields, field)
		}
		sort.Strings(thresholdFields)
		for _, field := range thresholdFields {
			if field == "last_update_time" {
				continue
			}
			if value, ok := parseCounterLike(thresholdData[field]); ok && collector.metricFilter.Enabled("sonic_transceiver_dom_threshold_value") {
				metrics = append(metrics, prometheus.MustNewConstMetric(collector.domThresholdValue, prometheus.GaugeValue, value, device, field))
			}
		}
	}

	return metrics, skippedEntries, truncated, nil
}

func (collector *transceiverCollector) appendTransceiverFlags(_ []prometheus.Metric, metrics *[]prometheus.Metric, valueDesc, changeDesc, setDesc, clearDesc *prometheus.Desc, device string, values, changes, setTimes, clearTimes map[string]string, emitValues, emitChanges, emitSet, emitClear bool) {
	fields := make([]string, 0, len(values))
	for field := range values {
		fields = append(fields, field)
	}
	sort.Strings(fields)

	for _, field := range fields {
		if value, ok := parseBoolish(values[field]); ok && emitValues {
			*metrics = append(*metrics, prometheus.MustNewConstMetric(valueDesc, prometheus.GaugeValue, value, device, field))
		}
		if value, ok := parseCounterLike(changes[field]); ok && emitChanges {
			*metrics = append(*metrics, prometheus.MustNewConstMetric(changeDesc, prometheus.CounterValue, value, device, field))
		}
		if value, ok := parseEventTime(setTimes[field]); ok && emitSet {
			*metrics = append(*metrics, prometheus.MustNewConstMetric(setDesc, prometheus.GaugeValue, value, device, field))
		}
		if value, ok := parseEventTime(clearTimes[field]); ok && emitClear {
			*metrics = append(*metrics, prometheus.MustNewConstMetric(clearDesc, prometheus.GaugeValue, value, device, field))
		}
	}
}
