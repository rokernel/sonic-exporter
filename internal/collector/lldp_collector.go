package collector

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/vinted/sonic-exporter/pkg/redis"
)

type lldpCollectorConfig struct {
	enabled         bool
	includeMgmt     bool
	refreshInterval time.Duration
	timeout         time.Duration
	maxNeighbors    int
	redisScanCount  int64
}

type lldpCollector struct {
	lldpNeighborInfo       *prometheus.Desc
	lldpNeighbors          *prometheus.Desc
	scrapeDuration         *prometheus.Desc
	scrapeCollectorSuccess *prometheus.Desc
	cacheAge               *prometheus.Desc
	skippedEntries         *prometheus.Desc

	logger *slog.Logger
	config lldpCollectorConfig

	mu                 sync.RWMutex
	cachedMetrics      []prometheus.Metric
	lastSuccess        float64
	lastScrapeDuration float64
	lastNeighbors      float64
	lastSkippedEntries float64
	lastRefreshTime    time.Time
}

func NewLldpCollector(logger *slog.Logger) *lldpCollector {
	const (
		namespace = "sonic"
		subsystem = "lldp"
	)

	collector := &lldpCollector{
		lldpNeighborInfo: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "neighbor_info"),
			"Non-numeric data about LLDP neighbor, value is always 1", []string{"local_interface", "local_role", "remote_system_name", "remote_port_id", "remote_port_desc", "remote_port_id_subtype", "remote_port_display", "remote_chassis_id", "remote_mgmt_ip"}, nil),
		lldpNeighbors: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "neighbors"),
			"Number of LLDP neighbors exported", nil, nil),
		scrapeDuration: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "scrape_duration_seconds"),
			"Time it took for exporter to refresh LLDP metrics", nil, nil),
		scrapeCollectorSuccess: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "collector_success"),
			"Whether LLDP collector succeeded", nil, nil),
		cacheAge: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "cache_age_seconds"),
			"Age of latest LLDP cache refresh", nil, nil),
		skippedEntries: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries_skipped"),
			"Number of LLDP entries skipped during latest refresh", nil, nil),
		logger: logger,
		config: loadLldpCollectorConfig(logger),
	}

	if !collector.config.enabled {
		collector.logger.Info("LLDP collector is disabled")
		return collector
	}

	collector.refreshMetrics()
	go collector.refreshLoop()

	return collector
}

func (collector *lldpCollector) IsEnabled() bool {
	return collector.config.enabled
}

func (collector *lldpCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- collector.lldpNeighborInfo
	ch <- collector.lldpNeighbors
	ch <- collector.scrapeDuration
	ch <- collector.scrapeCollectorSuccess
	ch <- collector.cacheAge
	ch <- collector.skippedEntries
}

func (collector *lldpCollector) Collect(ch chan<- prometheus.Metric) {
	if !collector.config.enabled {
		return
	}

	collector.mu.RLock()
	cachedMetrics := append([]prometheus.Metric{}, collector.cachedMetrics...)
	lastNeighbors := collector.lastNeighbors
	lastSkippedEntries := collector.lastSkippedEntries
	lastScrapeDuration := collector.lastScrapeDuration
	lastSuccess := collector.lastSuccess
	lastRefreshTime := collector.lastRefreshTime
	collector.mu.RUnlock()

	for _, metric := range cachedMetrics {
		ch <- metric
	}

	cacheAge := 0.0
	if !lastRefreshTime.IsZero() {
		cacheAge = time.Since(lastRefreshTime).Seconds()
	}

	ch <- prometheus.MustNewConstMetric(collector.lldpNeighbors, prometheus.GaugeValue, lastNeighbors)
	ch <- prometheus.MustNewConstMetric(collector.scrapeDuration, prometheus.GaugeValue, lastScrapeDuration)
	ch <- prometheus.MustNewConstMetric(collector.scrapeCollectorSuccess, prometheus.GaugeValue, lastSuccess)
	ch <- prometheus.MustNewConstMetric(collector.cacheAge, prometheus.GaugeValue, cacheAge)
	ch <- prometheus.MustNewConstMetric(collector.skippedEntries, prometheus.GaugeValue, lastSkippedEntries)
}

func (collector *lldpCollector) refreshLoop() {
	ticker := time.NewTicker(collector.config.refreshInterval)
	defer ticker.Stop()

	for range ticker.C {
		collector.refreshMetrics()
	}
}

func (collector *lldpCollector) refreshMetrics() {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), collector.config.timeout)
	defer cancel()

	metrics, neighbors, skippedEntries, err := collector.scrapeMetrics(ctx)
	scrapeDuration := time.Since(start).Seconds()

	collector.mu.Lock()
	defer collector.mu.Unlock()

	collector.lastScrapeDuration = scrapeDuration

	if err != nil {
		collector.lastSuccess = 0
		collector.logger.Error("Error refreshing LLDP metrics", "error", err)
		return
	}

	collector.cachedMetrics = metrics
	collector.lastNeighbors = float64(neighbors)
	collector.lastSkippedEntries = float64(skippedEntries)
	collector.lastSuccess = 1
	collector.lastRefreshTime = time.Now()
}

func (collector *lldpCollector) scrapeMetrics(ctx context.Context) ([]prometheus.Metric, int, int, error) {
	redisClient, err := redis.NewClient()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("redis client initialization failed: %w", err)
	}
	defer redisClient.Close()

	lldpKeys, err := redisClient.ScanKeysFromDb(ctx, "APPL_DB", "LLDP_ENTRY_TABLE:*", collector.config.redisScanCount)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to scan LLDP keys: %w", err)
	}

	sort.Strings(lldpKeys)

	metrics := make([]prometheus.Metric, 0, len(lldpKeys))
	neighbors := 0
	skippedEntries := 0

	for _, lldpKey := range lldpKeys {
		if neighbors >= collector.config.maxNeighbors {
			skippedEntries++
			continue
		}

		localInterface := strings.TrimPrefix(lldpKey, "LLDP_ENTRY_TABLE:")
		if localInterface == "" || localInterface == lldpKey {
			skippedEntries++
			continue
		}

		if !collector.config.includeMgmt && localInterface == "eth0" {
			continue
		}

		lldpData, err := redisClient.HgetAllFromDb(ctx, "APPL_DB", lldpKey)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("failed to read LLDP entry %s: %w", lldpKey, err)
		}

		if len(lldpData) == 0 {
			skippedEntries++
			continue
		}

		remoteSystemName := lldpData["lldp_rem_sys_name"]
		remotePortID := lldpData["lldp_rem_port_id"]
		remotePortDesc := lldpData["lldp_rem_port_desc"]
		remotePortIDSubtype := lldpData["lldp_rem_port_id_subtype"]
		remotePortDisplay := resolvedRemotePortDisplay(remotePortID, remotePortDesc, remotePortIDSubtype)
		remoteChassisID := lldpData["lldp_rem_chassis_id"]
		remoteMgmtIP := lldpData["lldp_rem_man_addr"]

		if remoteSystemName == "" && remotePortID == "" && remotePortDesc == "" && remoteChassisID == "" && remoteMgmtIP == "" {
			skippedEntries++
			continue
		}

		localRole := "frontpanel"
		if localInterface == "eth0" {
			localRole = "management"
		} else if !strings.HasPrefix(localInterface, "Ethernet") {
			localRole = "other"
		}

		metrics = append(metrics, prometheus.MustNewConstMetric(
			collector.lldpNeighborInfo, prometheus.GaugeValue, 1,
			localInterface,
			localRole,
			remoteSystemName,
			remotePortID,
			remotePortDesc,
			remotePortIDSubtype,
			remotePortDisplay,
			remoteChassisID,
			remoteMgmtIP,
		))
		neighbors++
	}

	return metrics, neighbors, skippedEntries, nil
}

func loadLldpCollectorConfig(logger *slog.Logger) lldpCollectorConfig {
	return lldpCollectorConfig{
		enabled:         parseBoolEnv(logger, "LLDP_ENABLED", true),
		includeMgmt:     parseBoolEnv(logger, "LLDP_INCLUDE_MGMT", true),
		refreshInterval: parseDurationEnv(logger, "LLDP_REFRESH_INTERVAL", 30*time.Second),
		timeout:         parseDurationEnv(logger, "LLDP_TIMEOUT", 2*time.Second),
		maxNeighbors:    parseIntEnv(logger, "LLDP_MAX_NEIGHBORS", 512),
		redisScanCount:  256,
	}
}

func parseBoolEnv(logger *slog.Logger, key string, defaultValue bool) bool {
	value, exists := os.LookupEnv(key)
	if !exists || value == "" {
		return defaultValue
	}

	parsedValue, err := strconv.ParseBool(value)
	if err != nil {
		logger.Warn("Invalid boolean value in env, using default", "key", key, "value", value, "default", defaultValue)
		return defaultValue
	}

	return parsedValue
}

func parseDurationEnv(logger *slog.Logger, key string, defaultValue time.Duration) time.Duration {
	value, exists := os.LookupEnv(key)
	if !exists || value == "" {
		return defaultValue
	}

	parsedValue, err := time.ParseDuration(value)
	if err != nil {
		logger.Warn("Invalid duration value in env, using default", "key", key, "value", value, "default", defaultValue.String())
		return defaultValue
	}

	if parsedValue <= 0 {
		logger.Warn("Duration env must be greater than zero, using default", "key", key, "value", value, "default", defaultValue.String())
		return defaultValue
	}

	return parsedValue
}

func parseIntEnv(logger *slog.Logger, key string, defaultValue int) int {
	value, exists := os.LookupEnv(key)
	if !exists || value == "" {
		return defaultValue
	}

	parsedValue, err := strconv.Atoi(value)
	if err != nil {
		logger.Warn("Invalid integer value in env, using default", "key", key, "value", value, "default", defaultValue)
		return defaultValue
	}

	if parsedValue <= 0 {
		logger.Warn("Integer env must be greater than zero, using default", "key", key, "value", value, "default", defaultValue)
		return defaultValue
	}

	return parsedValue
}

func resolvedRemotePortDisplay(remotePortID, remotePortDesc, remotePortIDSubtype string) string {
	subtype := strings.ToLower(strings.TrimSpace(remotePortIDSubtype))

	if remotePortDesc != "" && (subtype == "7" || subtype == "local" || subtype == "3" || subtype == "mac") {
		return remotePortDesc
	}

	if remotePortID != "" {
		return remotePortID
	}

	return remotePortDesc
}
