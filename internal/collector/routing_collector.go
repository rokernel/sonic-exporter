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

type routingCollectorConfig struct {
	enabled         bool
	refreshInterval time.Duration
	timeout         time.Duration
	maxNeighbors    int
	maxRoutes       int
	redisScanCount  int64
}

type routingCollector struct {
	neighborEntries        *prometheus.Desc
	routeEntries           *prometheus.Desc
	entriesSkipped         *prometheus.Desc
	entriesTruncated       *prometheus.Desc
	scrapeDuration         *prometheus.Desc
	scrapeCollectorSuccess *prometheus.Desc
	cacheAge               *prometheus.Desc

	logger       *slog.Logger
	metricFilter MetricFilter
	config       routingCollectorConfig

	mu                 sync.RWMutex
	cachedMetrics      []prometheus.Metric
	lastSuccess        float64
	lastScrapeDuration float64
	lastSkippedEntries float64
	lastTruncated      float64
	lastRefreshTime    time.Time
}

func NewRoutingCollector(logger *slog.Logger, metricFilter MetricFilter) *routingCollector {
	const (
		namespace = "sonic"
		subsystem = "routing"
	)

	collector := &routingCollector{
		neighborEntries: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "neighbor_entries"),
			"Number of neighbor entries by interface and address family", []string{"interface", "family"}, nil),
		routeEntries: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "route_entries"),
			"Number of route entries by address family and protocol", []string{"family", "protocol"}, nil),
		entriesSkipped: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries_skipped"),
			"Number of routing entries skipped during latest refresh", nil, nil),
		entriesTruncated: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries_truncated"),
			"Whether routing collection hit entry limits (1=yes, 0=no)", nil, nil),
		scrapeDuration: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "scrape_duration_seconds"),
			"Time it took for exporter to refresh routing metrics", nil, nil),
		scrapeCollectorSuccess: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "collector_success"),
			"Whether routing collector succeeded", nil, nil),
		cacheAge: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "cache_age_seconds"),
			"Age of latest routing cache refresh", nil, nil),
		logger:       logger,
		metricFilter: metricFilter,
		config: routingCollectorConfig{
			enabled:         parseBoolEnv(logger, "ROUTING_ENABLED", false),
			refreshInterval: parseDurationEnv(logger, "ROUTING_REFRESH_INTERVAL", 60*time.Second),
			timeout:         parseDurationEnv(logger, "ROUTING_TIMEOUT", 2*time.Second),
			maxNeighbors:    parseIntEnv(logger, "ROUTING_MAX_NEIGHBORS", 50000),
			maxRoutes:       parseIntEnv(logger, "ROUTING_MAX_ROUTES", 200000),
			redisScanCount:  256,
		},
	}

	if !collector.config.enabled {
		collector.logger.Info("Routing collector is disabled")
		return collector
	}

	collector.refreshMetrics()
	go collector.refreshLoop()

	return collector
}

func (collector *routingCollector) IsEnabled() bool {
	return collector.config.enabled
}

func (collector *routingCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- collector.neighborEntries
	ch <- collector.routeEntries
	ch <- collector.entriesSkipped
	ch <- collector.entriesTruncated
	ch <- collector.scrapeDuration
	ch <- collector.scrapeCollectorSuccess
	ch <- collector.cacheAge
}

func (collector *routingCollector) Collect(ch chan<- prometheus.Metric) {
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

	if collector.metricFilter.Enabled("sonic_routing_entries_skipped") {
		ch <- prometheus.MustNewConstMetric(collector.entriesSkipped, prometheus.GaugeValue, lastSkippedEntries)
	}
	if collector.metricFilter.Enabled("sonic_routing_entries_truncated") {
		ch <- prometheus.MustNewConstMetric(collector.entriesTruncated, prometheus.GaugeValue, lastTruncated)
	}
	if collector.metricFilter.Enabled("sonic_routing_scrape_duration_seconds") {
		ch <- prometheus.MustNewConstMetric(collector.scrapeDuration, prometheus.GaugeValue, lastScrapeDuration)
	}
	if collector.metricFilter.Enabled("sonic_routing_collector_success") {
		ch <- prometheus.MustNewConstMetric(collector.scrapeCollectorSuccess, prometheus.GaugeValue, lastSuccess)
	}
	if collector.metricFilter.Enabled("sonic_routing_cache_age_seconds") {
		ch <- prometheus.MustNewConstMetric(collector.cacheAge, prometheus.GaugeValue, cacheAge)
	}
}

func (collector *routingCollector) refreshLoop() {
	ticker := time.NewTicker(collector.config.refreshInterval)
	defer ticker.Stop()

	for range ticker.C {
		collector.refreshMetrics()
	}
}

func (collector *routingCollector) refreshMetrics() {
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
		collector.logger.Error("Error refreshing routing metrics", "error", err)
		return
	}

	collector.cachedMetrics = metrics
	collector.lastSkippedEntries = float64(skippedEntries)
	collector.lastTruncated = truncated
	collector.lastSuccess = 1
	collector.lastRefreshTime = time.Now()
}

func (collector *routingCollector) scrapeMetrics(ctx context.Context) ([]prometheus.Metric, int, float64, error) {
	redisClient, err := redis.NewClient()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("redis client initialization failed: %w", err)
	}
	defer redisClient.Close()

	neighborKeys, err := redisClient.ScanKeysFromDb(ctx, "APPL_DB", "NEIGH_TABLE:*", collector.config.redisScanCount)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to scan neighbor keys: %w", err)
	}

	routeKeys, err := redisClient.ScanKeysFromDb(ctx, "APPL_DB", "ROUTE_TABLE:*", collector.config.redisScanCount)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to scan route keys: %w", err)
	}

	sort.Strings(neighborKeys)
	sort.Strings(routeKeys)

	neighborCounts := map[string]float64{}
	routeCounts := map[string]float64{}
	skippedEntries := 0
	truncated := 0.0

	for index, neighborKey := range neighborKeys {
		if index >= collector.config.maxNeighbors {
			truncated = 1
			skippedEntries += len(neighborKeys) - index
			break
		}

		suffix, err := parseKeySuffix(neighborKey, "NEIGH_TABLE:")
		if err != nil {
			skippedEntries++
			continue
		}
		parts := strings.SplitN(suffix, ":", 2)
		if len(parts) != 2 {
			skippedEntries++
			continue
		}

		neighborData, err := redisClient.HgetAllFromDb(ctx, "APPL_DB", neighborKey)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("failed to read neighbor entry %s: %w", neighborKey, err)
		}
		if len(neighborData) == 0 {
			skippedEntries++
			continue
		}

		family := strings.ToLower(strings.TrimSpace(neighborData["family"]))
		if family == "" {
			family = familyFromPrefix(parts[1])
		}

		neighborCounts[parts[0]+"|"+family]++
	}

	for index, routeKey := range routeKeys {
		if index >= collector.config.maxRoutes {
			truncated = 1
			skippedEntries += len(routeKeys) - index
			break
		}

		prefix, err := parseKeySuffix(routeKey, "ROUTE_TABLE:")
		if err != nil {
			skippedEntries++
			continue
		}

		routeData, err := redisClient.HgetAllFromDb(ctx, "APPL_DB", routeKey)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("failed to read route entry %s: %w", routeKey, err)
		}
		if len(routeData) == 0 {
			skippedEntries++
			continue
		}

		protocol := strings.ToLower(strings.TrimSpace(routeData["protocol"]))
		if protocol == "" {
			protocol = "unknown"
		}

		routeCounts[familyFromPrefix(prefix)+"|"+protocol]++
	}

	metrics := make([]prometheus.Metric, 0, len(neighborCounts)+len(routeCounts))
	for _, key := range sortedMapKeys(neighborCounts) {
		parts := strings.Split(key, "|")
		if collector.metricFilter.Enabled("sonic_routing_neighbor_entries") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.neighborEntries, prometheus.GaugeValue, neighborCounts[key], parts[0], parts[1]))
		}
	}

	for _, key := range sortedMapKeys(routeCounts) {
		parts := strings.Split(key, "|")
		if collector.metricFilter.Enabled("sonic_routing_route_entries") {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.routeEntries, prometheus.GaugeValue, routeCounts[key], parts[0], parts[1]))
		}
	}

	return metrics, skippedEntries, truncated, nil
}
