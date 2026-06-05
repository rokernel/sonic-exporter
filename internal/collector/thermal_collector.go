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

type thermalCollectorConfig struct {
	enabled         bool
	refreshInterval time.Duration
	timeout         time.Duration
	redisScanCount  int64
}

type thermalCollector struct {
	asicTemperature        *prometheus.Desc
	asicAverageTemperature *prometheus.Desc
	asicMaximumTemperature *prometheus.Desc
	sfpMaximumTemperature  *prometheus.Desc
	entriesSkipped         *prometheus.Desc
	scrapeDuration         *prometheus.Desc
	scrapeCollectorSuccess *prometheus.Desc
	cacheAge               *prometheus.Desc

	logger       *slog.Logger
	metricFilter MetricFilter
	config       thermalCollectorConfig

	mu                 sync.RWMutex
	cachedMetrics      []prometheus.Metric
	lastSuccess        float64
	lastScrapeDuration float64
	lastSkippedEntries float64
	lastRefreshTime    time.Time
}

func NewThermalCollector(logger *slog.Logger, metricFilter MetricFilter) *thermalCollector {
	const (
		namespace = "sonic"
		subsystem = "thermal"
	)

	collector := &thermalCollector{
		asicTemperature: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "asic_temperature_celsius"),
			"ASIC per-sensor temperature in celsius", []string{"sensor"}, nil),
		asicAverageTemperature: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "asic_average_temperature_celsius"),
			"ASIC average temperature in celsius", nil, nil),
		asicMaximumTemperature: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "asic_maximum_temperature_celsius"),
			"ASIC maximum temperature in celsius", nil, nil),
		sfpMaximumTemperature: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "sfp_maximum_temperature_celsius"),
			"Maximum transceiver temperature across all optics", nil, nil),
		entriesSkipped: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries_skipped"),
			"Number of thermal entries skipped during latest refresh", nil, nil),
		scrapeDuration: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "scrape_duration_seconds"),
			"Time it took for exporter to refresh thermal metrics", nil, nil),
		scrapeCollectorSuccess: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "collector_success"),
			"Whether thermal collector succeeded", nil, nil),
		cacheAge: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "cache_age_seconds"),
			"Age of latest thermal cache refresh", nil, nil),
		logger:       logger,
		metricFilter: metricFilter,
		config: thermalCollectorConfig{
			enabled:         parseBoolEnv(logger, "THERMAL_ENABLED", true),
			refreshInterval: parseDurationEnv(logger, "THERMAL_REFRESH_INTERVAL", 60*time.Second),
			timeout:         parseDurationEnv(logger, "THERMAL_TIMEOUT", 2*time.Second),
			redisScanCount:  32,
		},
	}

	if !collector.config.enabled {
		collector.logger.Info("Thermal collector is disabled")
		return collector
	}

	collector.refreshMetrics()
	go collector.refreshLoop()

	return collector
}

func (collector *thermalCollector) IsEnabled() bool { return collector.config.enabled }

func (collector *thermalCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- collector.asicTemperature
	ch <- collector.asicAverageTemperature
	ch <- collector.asicMaximumTemperature
	ch <- collector.sfpMaximumTemperature
	ch <- collector.entriesSkipped
	ch <- collector.scrapeDuration
	ch <- collector.scrapeCollectorSuccess
	ch <- collector.cacheAge
}

func (collector *thermalCollector) Collect(ch chan<- prometheus.Metric) {
	if !collector.config.enabled {
		return
	}

	collector.mu.RLock()
	cachedMetrics := append([]prometheus.Metric{}, collector.cachedMetrics...)
	lastScrapeDuration := collector.lastScrapeDuration
	lastSuccess := collector.lastSuccess
	lastSkippedEntries := collector.lastSkippedEntries
	lastRefreshTime := collector.lastRefreshTime
	collector.mu.RUnlock()

	for _, metric := range cachedMetrics {
		ch <- metric
	}

	cacheAge := 0.0
	if !lastRefreshTime.IsZero() {
		cacheAge = time.Since(lastRefreshTime).Seconds()
	}
	if collector.metricFilter.Enabled("sonic_thermal_entries_skipped") {
		ch <- prometheus.MustNewConstMetric(collector.entriesSkipped, prometheus.GaugeValue, lastSkippedEntries)
	}
	if collector.metricFilter.Enabled("sonic_thermal_scrape_duration_seconds") {
		ch <- prometheus.MustNewConstMetric(collector.scrapeDuration, prometheus.GaugeValue, lastScrapeDuration)
	}
	if collector.metricFilter.Enabled("sonic_thermal_collector_success") {
		ch <- prometheus.MustNewConstMetric(collector.scrapeCollectorSuccess, prometheus.GaugeValue, lastSuccess)
	}
	if collector.metricFilter.Enabled("sonic_thermal_cache_age_seconds") {
		ch <- prometheus.MustNewConstMetric(collector.cacheAge, prometheus.GaugeValue, cacheAge)
	}
}

func (collector *thermalCollector) refreshLoop() {
	ticker := time.NewTicker(collector.config.refreshInterval)
	defer ticker.Stop()
	for range ticker.C {
		collector.refreshMetrics()
	}
}

func (collector *thermalCollector) refreshMetrics() {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), collector.config.timeout)
	defer cancel()
	metrics, skippedEntries, err := collector.scrapeMetrics(ctx)
	scrapeDuration := time.Since(start).Seconds()

	collector.mu.Lock()
	defer collector.mu.Unlock()
	collector.lastScrapeDuration = scrapeDuration
	if err != nil {
		collector.lastSuccess = 0
		collector.logger.Error("Error refreshing thermal metrics", "error", err)
		return
	}
	collector.cachedMetrics = metrics
	collector.lastSkippedEntries = float64(skippedEntries)
	collector.lastSuccess = 1
	collector.lastRefreshTime = time.Now()
}

func (collector *thermalCollector) scrapeMetrics(ctx context.Context) ([]prometheus.Metric, int, error) {
	redisClient, err := redis.NewClient()
	if err != nil {
		return nil, 0, fmt.Errorf("redis client initialization failed: %w", err)
	}
	defer redisClient.Close()

	metrics := []prometheus.Metric{}
	skippedEntries := 0

	asicTemperatureData, err := redisClient.HgetAllFromDb(ctx, "STATE_DB", "ASIC_TEMPERATURE_INFO")
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read ASIC_TEMPERATURE_INFO: %w", err)
	}

	asicFields := make([]string, 0, len(asicTemperatureData))
	for field := range asicTemperatureData {
		asicFields = append(asicFields, field)
	}
	sort.Strings(asicFields)
	for _, field := range asicFields {
		value := asicTemperatureData[field]
		parsedValue, ok := parseCounterLike(value)
		if !ok {
			skippedEntries++
			continue
		}

		switch {
		case strings.HasPrefix(field, "temperature_"):
			if collector.metricFilter.Enabled("sonic_thermal_asic_temperature_celsius") {
				metrics = append(metrics, prometheus.MustNewConstMetric(collector.asicTemperature, prometheus.GaugeValue, parsedValue, field))
			}
		case field == "average_temperature":
			if collector.metricFilter.Enabled("sonic_thermal_asic_average_temperature_celsius") {
				metrics = append(metrics, prometheus.MustNewConstMetric(collector.asicAverageTemperature, prometheus.GaugeValue, parsedValue))
			}
		case field == "maximum_temperature":
			if collector.metricFilter.Enabled("sonic_thermal_asic_maximum_temperature_celsius") {
				metrics = append(metrics, prometheus.MustNewConstMetric(collector.asicMaximumTemperature, prometheus.GaugeValue, parsedValue))
			}
		}
	}

	sfpMaxData, err := redisClient.HgetAllFromDb(ctx, "STATE_DB", "TEMPERATURE_SFP_MAX")
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read TEMPERATURE_SFP_MAX: %w", err)
	}
	if value, ok := parseCounterLike(sfpMaxData["maximum_temperature"]); ok {
		if collector.metricFilter.Enabled("sonic_thermal_sfp_maximum_temperature_celsius") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.sfpMaximumTemperature, prometheus.GaugeValue, value))
		}
	}

	return metrics, skippedEntries, nil
}
