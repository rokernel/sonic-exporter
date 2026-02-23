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

type lagCollectorConfig struct {
	enabled         bool
	refreshInterval time.Duration
	timeout         time.Duration
	maxLags         int
	maxMembers      int
	redisScanCount  int64
}

type lagCollector struct {
	lagInfo                *prometheus.Desc
	lagAdminStatus         *prometheus.Desc
	lagOperStatus          *prometheus.Desc
	lagMembers             *prometheus.Desc
	lagMemberStatus        *prometheus.Desc
	scrapeDuration         *prometheus.Desc
	scrapeCollectorSuccess *prometheus.Desc
	cacheAge               *prometheus.Desc
	skippedEntries         *prometheus.Desc

	logger *slog.Logger
	config lagCollectorConfig

	mu                 sync.RWMutex
	cachedMetrics      []prometheus.Metric
	lastSuccess        float64
	lastScrapeDuration float64
	lastSkippedEntries float64
	lastRefreshTime    time.Time
}

type lagMemberEntry struct {
	name   string
	status string
}

func NewLagCollector(logger *slog.Logger) *lagCollector {
	const (
		namespace = "sonic"
		subsystem = "lag"
	)

	collector := &lagCollector{
		lagInfo: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "info"),
			"Non-numeric data about LAG, value is always 1", []string{"lag"}, nil),
		lagAdminStatus: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "admin_status"),
			"Administrative state of LAG (1=up, 0=down)", []string{"lag"}, nil),
		lagOperStatus: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "oper_status"),
			"Operational state of LAG (1=up, 0=down)", []string{"lag"}, nil),
		lagMembers: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "members"),
			"Number of LAG member interfaces", []string{"lag"}, nil),
		lagMemberStatus: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "member_status"),
			"Status of LAG member interface (1=enabled, 0=disabled)", []string{"lag", "member"}, nil),
		scrapeDuration: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "scrape_duration_seconds"),
			"Time it took for exporter to refresh LAG metrics", nil, nil),
		scrapeCollectorSuccess: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "collector_success"),
			"Whether LAG collector succeeded", nil, nil),
		cacheAge: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "cache_age_seconds"),
			"Age of latest LAG cache refresh", nil, nil),
		skippedEntries: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries_skipped"),
			"Number of LAG entries skipped during latest refresh", nil, nil),
		logger: logger,
		config: loadLagCollectorConfig(logger),
	}

	if !collector.config.enabled {
		collector.logger.Info("LAG collector is disabled")
		return collector
	}

	collector.refreshMetrics()
	go collector.refreshLoop()

	return collector
}

func (collector *lagCollector) IsEnabled() bool {
	return collector.config.enabled
}

func (collector *lagCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- collector.lagInfo
	ch <- collector.lagAdminStatus
	ch <- collector.lagOperStatus
	ch <- collector.lagMembers
	ch <- collector.lagMemberStatus
	ch <- collector.scrapeDuration
	ch <- collector.scrapeCollectorSuccess
	ch <- collector.cacheAge
	ch <- collector.skippedEntries
}

func (collector *lagCollector) Collect(ch chan<- prometheus.Metric) {
	if !collector.config.enabled {
		return
	}

	collector.mu.RLock()
	cachedMetrics := append([]prometheus.Metric{}, collector.cachedMetrics...)
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

	ch <- prometheus.MustNewConstMetric(collector.scrapeDuration, prometheus.GaugeValue, lastScrapeDuration)
	ch <- prometheus.MustNewConstMetric(collector.scrapeCollectorSuccess, prometheus.GaugeValue, lastSuccess)
	ch <- prometheus.MustNewConstMetric(collector.cacheAge, prometheus.GaugeValue, cacheAge)
	ch <- prometheus.MustNewConstMetric(collector.skippedEntries, prometheus.GaugeValue, lastSkippedEntries)
}

func (collector *lagCollector) refreshLoop() {
	ticker := time.NewTicker(collector.config.refreshInterval)
	defer ticker.Stop()

	for range ticker.C {
		collector.refreshMetrics()
	}
}

func (collector *lagCollector) refreshMetrics() {
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
		collector.logger.Error("Error refreshing LAG metrics", "error", err)
		return
	}

	collector.cachedMetrics = metrics
	collector.lastSkippedEntries = float64(skippedEntries)
	collector.lastSuccess = 1
	collector.lastRefreshTime = time.Now()
}

func (collector *lagCollector) scrapeMetrics(ctx context.Context) ([]prometheus.Metric, int, error) {
	redisClient, err := redis.NewClient()
	if err != nil {
		return nil, 0, fmt.Errorf("redis client initialization failed: %w", err)
	}
	defer redisClient.Close()

	configLags, err := redisClient.ScanKeysFromDb(ctx, "CONFIG_DB", "PORTCHANNEL|*", collector.config.redisScanCount)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to scan PORTCHANNEL keys: %w", err)
	}

	applLags, err := redisClient.ScanKeysFromDb(ctx, "APPL_DB", "LAG_TABLE:*", collector.config.redisScanCount)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to scan LAG_TABLE keys: %w", err)
	}

	memberKeys, err := redisClient.ScanKeysFromDb(ctx, "APPL_DB", "LAG_MEMBER_TABLE:*", collector.config.redisScanCount)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to scan LAG_MEMBER_TABLE keys: %w", err)
	}

	lags := map[string]struct{}{}
	skippedEntries := 0

	for _, key := range configLags {
		lagName := strings.TrimPrefix(key, "PORTCHANNEL|")
		if lagName == "" || lagName == key {
			skippedEntries++
			continue
		}
		lags[lagName] = struct{}{}
	}

	for _, key := range applLags {
		lagName := strings.TrimPrefix(key, "LAG_TABLE:")
		if lagName == "" || lagName == key {
			skippedEntries++
			continue
		}
		lags[lagName] = struct{}{}
	}

	lagNames := make([]string, 0, len(lags))
	for lagName := range lags {
		lagNames = append(lagNames, lagName)
	}
	sort.Strings(lagNames)

	membersByLag := map[string][]lagMemberEntry{}
	for _, key := range memberKeys {
		memberPath := strings.TrimPrefix(key, "LAG_MEMBER_TABLE:")
		if memberPath == "" || memberPath == key {
			skippedEntries++
			continue
		}

		parts := strings.SplitN(memberPath, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			skippedEntries++
			continue
		}

		memberData, err := redisClient.HgetAllFromDb(ctx, "APPL_DB", key)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to read %s: %w", key, err)
		}

		membersByLag[parts[0]] = append(membersByLag[parts[0]], lagMemberEntry{name: parts[1], status: memberData["status"]})
	}

	metrics := make([]prometheus.Metric, 0)
	processedLags := 0
	processedMembers := 0

	for _, lagName := range lagNames {
		if processedLags >= collector.config.maxLags {
			skippedEntries++
			continue
		}

		configData, err := redisClient.HgetAllFromDb(ctx, "CONFIG_DB", "PORTCHANNEL|"+lagName)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to read CONFIG_DB PORTCHANNEL|%s: %w", lagName, err)
		}

		applData, err := redisClient.HgetAllFromDb(ctx, "APPL_DB", "LAG_TABLE:"+lagName)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to read APPL_DB LAG_TABLE:%s: %w", lagName, err)
		}

		metrics = append(metrics, prometheus.MustNewConstMetric(collector.lagInfo, prometheus.GaugeValue, 1, lagName))

		adminStatus := firstNonEmpty(applData["admin_status"], configData["admin_status"])
		if adminStatus != "" {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.lagAdminStatus, prometheus.GaugeValue, statusToGauge(adminStatus), lagName))
		}

		if operStatus := applData["oper_status"]; operStatus != "" {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.lagOperStatus, prometheus.GaugeValue, statusToGauge(operStatus), lagName))
		}

		members := membersByLag[lagName]
		sort.Slice(members, func(i, j int) bool {
			return members[i].name < members[j].name
		})

		memberCount := 0
		for _, member := range members {
			if processedMembers >= collector.config.maxMembers {
				skippedEntries++
				continue
			}

			metrics = append(metrics, prometheus.MustNewConstMetric(
				collector.lagMemberStatus,
				prometheus.GaugeValue,
				statusToGauge(member.status),
				lagName,
				member.name,
			))

			processedMembers++
			memberCount++
		}

		metrics = append(metrics, prometheus.MustNewConstMetric(collector.lagMembers, prometheus.GaugeValue, float64(memberCount), lagName))
		processedLags++
	}

	return metrics, skippedEntries, nil
}

func loadLagCollectorConfig(logger *slog.Logger) lagCollectorConfig {
	return lagCollectorConfig{
		enabled:         parseBoolEnv(logger, "LAG_ENABLED", true),
		refreshInterval: parseDurationEnv(logger, "LAG_REFRESH_INTERVAL", 30*time.Second),
		timeout:         parseDurationEnv(logger, "LAG_TIMEOUT", 2*time.Second),
		maxLags:         parseIntEnv(logger, "LAG_MAX_LAGS", 512),
		maxMembers:      parseIntEnv(logger, "LAG_MAX_MEMBERS", 4096),
		redisScanCount:  256,
	}
}

func statusToGauge(status string) float64 {
	value := strings.ToLower(strings.TrimSpace(status))
	if value == "up" || value == "enabled" || value == "selected" || value == "active" || value == "true" || value == "ok" {
		return 1
	}

	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}

	return ""
}
