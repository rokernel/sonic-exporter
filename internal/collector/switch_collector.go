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

type switchCollectorConfig struct {
	enabled         bool
	refreshInterval time.Duration
	timeout         time.Duration
	maxEntries      int
	redisScanCount  int64
}

type switchCollector struct {
	switchInfo             *prometheus.Desc
	switchFDBAgingSeconds  *prometheus.Desc
	switchEcmpHashSeed     *prometheus.Desc
	switchEcmpHashOffset   *prometheus.Desc
	switchLagHashSeed      *prometheus.Desc
	switchLagHashOffset    *prometheus.Desc
	switchOrderedEcmp      *prometheus.Desc
	entriesSkipped         *prometheus.Desc
	entriesTruncated       *prometheus.Desc
	scrapeDuration         *prometheus.Desc
	scrapeCollectorSuccess *prometheus.Desc
	cacheAge               *prometheus.Desc

	logger       *slog.Logger
	metricFilter MetricFilter
	config       switchCollectorConfig

	mu                 sync.RWMutex
	cachedMetrics      []prometheus.Metric
	lastSuccess        float64
	lastScrapeDuration float64
	lastSkippedEntries float64
	lastTruncated      float64
	lastRefreshTime    time.Time
}

func NewSwitchCollector(logger *slog.Logger, metricFilter MetricFilter) *switchCollector {
	const (
		namespace = "sonic"
		subsystem = "switch"
	)

	collector := &switchCollector{
		switchInfo: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "info"),
			"Switch table metadata, value is always 1", []string{"switch"}, nil),
		switchFDBAgingSeconds: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "fdb_aging_time_seconds"),
			"FDB aging time from SWITCH_TABLE", []string{"switch"}, nil),
		switchEcmpHashSeed: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "ecmp_hash_seed"),
			"ECMP hash seed from SWITCH_TABLE", []string{"switch"}, nil),
		switchEcmpHashOffset: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "ecmp_hash_offset"),
			"ECMP hash offset from SWITCH_TABLE", []string{"switch"}, nil),
		switchLagHashSeed: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "lag_hash_seed"),
			"LAG hash seed from SWITCH_TABLE", []string{"switch"}, nil),
		switchLagHashOffset: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "lag_hash_offset"),
			"LAG hash offset from SWITCH_TABLE", []string{"switch"}, nil),
		switchOrderedEcmp: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "ordered_ecmp"),
			"Whether ordered ECMP is enabled (1=yes, 0=no)", []string{"switch"}, nil),
		entriesSkipped: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries_skipped"),
			"Number of switch entries skipped during latest refresh", nil, nil),
		entriesTruncated: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries_truncated"),
			"Whether switch collection hit entry limits (1=yes, 0=no)", nil, nil),
		scrapeDuration: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "scrape_duration_seconds"),
			"Time it took for exporter to refresh switch metrics", nil, nil),
		scrapeCollectorSuccess: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "collector_success"),
			"Whether switch collector succeeded", nil, nil),
		cacheAge: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "cache_age_seconds"),
			"Age of latest switch cache refresh", nil, nil),
		logger:       logger,
		metricFilter: metricFilter,
		config: switchCollectorConfig{
			enabled:         parseBoolEnv(logger, "SWITCH_ENABLED", true),
			refreshInterval: parseDurationEnv(logger, "SWITCH_REFRESH_INTERVAL", 60*time.Second),
			timeout:         parseDurationEnv(logger, "SWITCH_TIMEOUT", 2*time.Second),
			maxEntries:      parseIntEnv(logger, "SWITCH_MAX_ENTRIES", 16),
			redisScanCount:  32,
		},
	}

	if !collector.config.enabled {
		collector.logger.Info("Switch collector is disabled")
		return collector
	}

	collector.refreshMetrics()
	go collector.refreshLoop()

	return collector
}

func (collector *switchCollector) IsEnabled() bool { return collector.config.enabled }

func (collector *switchCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- collector.switchInfo
	ch <- collector.switchFDBAgingSeconds
	ch <- collector.switchEcmpHashSeed
	ch <- collector.switchEcmpHashOffset
	ch <- collector.switchLagHashSeed
	ch <- collector.switchLagHashOffset
	ch <- collector.switchOrderedEcmp
	ch <- collector.entriesSkipped
	ch <- collector.entriesTruncated
	ch <- collector.scrapeDuration
	ch <- collector.scrapeCollectorSuccess
	ch <- collector.cacheAge
}

func (collector *switchCollector) Collect(ch chan<- prometheus.Metric) {
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
	if collector.metricFilter.Enabled("sonic_switch_entries_skipped") {
		ch <- prometheus.MustNewConstMetric(collector.entriesSkipped, prometheus.GaugeValue, lastSkippedEntries)
	}
	if collector.metricFilter.Enabled("sonic_switch_entries_truncated") {
		ch <- prometheus.MustNewConstMetric(collector.entriesTruncated, prometheus.GaugeValue, lastTruncated)
	}
	if collector.metricFilter.Enabled("sonic_switch_scrape_duration_seconds") {
		ch <- prometheus.MustNewConstMetric(collector.scrapeDuration, prometheus.GaugeValue, lastScrapeDuration)
	}
	if collector.metricFilter.Enabled("sonic_switch_collector_success") {
		ch <- prometheus.MustNewConstMetric(collector.scrapeCollectorSuccess, prometheus.GaugeValue, lastSuccess)
	}
	if collector.metricFilter.Enabled("sonic_switch_cache_age_seconds") {
		ch <- prometheus.MustNewConstMetric(collector.cacheAge, prometheus.GaugeValue, cacheAge)
	}
}

func (collector *switchCollector) refreshLoop() {
	ticker := time.NewTicker(collector.config.refreshInterval)
	defer ticker.Stop()
	for range ticker.C {
		collector.refreshMetrics()
	}
}

func (collector *switchCollector) refreshMetrics() {
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
		collector.logger.Error("Error refreshing switch metrics", "error", err)
		return
	}
	collector.cachedMetrics = metrics
	collector.lastSkippedEntries = float64(skippedEntries)
	collector.lastTruncated = truncated
	collector.lastSuccess = 1
	collector.lastRefreshTime = time.Now()
}

func (collector *switchCollector) scrapeMetrics(ctx context.Context) ([]prometheus.Metric, int, float64, error) {
	redisClient, err := redis.NewClient()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("redis client initialization failed: %w", err)
	}
	defer redisClient.Close()

	switchKeys, err := redisClient.ScanKeysFromDb(ctx, "APPL_DB", "SWITCH_TABLE:*", collector.config.redisScanCount)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to scan switch keys: %w", err)
	}
	sort.Strings(switchKeys)

	metrics := make([]prometheus.Metric, 0, len(switchKeys)*7)
	skippedEntries := 0
	truncated := 0.0

	for index, switchKey := range switchKeys {
		if index >= collector.config.maxEntries {
			truncated = 1
			skippedEntries += len(switchKeys) - index
			break
		}

		switchName, err := parseKeySuffix(switchKey, "SWITCH_TABLE:")
		if err != nil {
			skippedEntries++
			continue
		}

		switchData, err := redisClient.HgetAllFromDb(ctx, "APPL_DB", switchKey)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("failed to read switch entry %s: %w", switchKey, err)
		}
		if len(switchData) == 0 {
			skippedEntries++
			continue
		}

		if collector.metricFilter.Enabled("sonic_switch_info") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.switchInfo, prometheus.GaugeValue, 1, switchName))
		}
		if value, ok := parseCounterLike(switchData["fdb_aging_time"]); ok && collector.metricFilter.Enabled("sonic_switch_fdb_aging_time_seconds") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.switchFDBAgingSeconds, prometheus.GaugeValue, value, switchName))
		}
		if value, ok := parseCounterLike(switchData["ecmp_hash_seed"]); ok && collector.metricFilter.Enabled("sonic_switch_ecmp_hash_seed") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.switchEcmpHashSeed, prometheus.GaugeValue, value, switchName))
		}
		if value, ok := parseCounterLike(switchData["ecmp_hash_offset"]); ok && collector.metricFilter.Enabled("sonic_switch_ecmp_hash_offset") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.switchEcmpHashOffset, prometheus.GaugeValue, value, switchName))
		}
		if value, ok := parseCounterLike(switchData["lag_hash_seed"]); ok && collector.metricFilter.Enabled("sonic_switch_lag_hash_seed") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.switchLagHashSeed, prometheus.GaugeValue, value, switchName))
		}
		if value, ok := parseCounterLike(switchData["lag_hash_offset"]); ok && collector.metricFilter.Enabled("sonic_switch_lag_hash_offset") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.switchLagHashOffset, prometheus.GaugeValue, value, switchName))
		}
		if value, ok := parseBoolish(switchData["ordered_ecmp"]); ok && collector.metricFilter.Enabled("sonic_switch_ordered_ecmp") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.switchOrderedEcmp, prometheus.GaugeValue, value, switchName))
		}
	}

	return metrics, skippedEntries, truncated, nil
}
