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

type vlanCollectorConfig struct {
	enabled         bool
	refreshInterval time.Duration
	timeout         time.Duration
	maxVlans        int
	maxMembers      int
	redisScanCount  int64
}

type vlanCollector struct {
	vlanInfo               *prometheus.Desc
	vlanAdminStatus        *prometheus.Desc
	vlanOperStatus         *prometheus.Desc
	vlanMembers            *prometheus.Desc
	vlanMemberInfo         *prometheus.Desc
	scrapeDuration         *prometheus.Desc
	scrapeCollectorSuccess *prometheus.Desc
	cacheAge               *prometheus.Desc
	skippedEntries         *prometheus.Desc

	logger *slog.Logger
	config vlanCollectorConfig

	mu                 sync.RWMutex
	cachedMetrics      []prometheus.Metric
	lastSuccess        float64
	lastScrapeDuration float64
	lastSkippedEntries float64
	lastRefreshTime    time.Time
}

type vlanMemberEntry struct {
	name        string
	taggingMode string
}

func NewVlanCollector(logger *slog.Logger) *vlanCollector {
	const (
		namespace = "sonic"
		subsystem = "vlan"
	)

	collector := &vlanCollector{
		vlanInfo: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "info"),
			"Non-numeric data about VLAN, value is always 1", []string{"vlan", "vlan_id"}, nil),
		vlanAdminStatus: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "admin_status"),
			"Administrative state of VLAN (1=up, 0=down)", []string{"vlan"}, nil),
		vlanOperStatus: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "oper_status"),
			"Operational state of VLAN (1=up, 0=down)", []string{"vlan"}, nil),
		vlanMembers: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "members"),
			"Number of VLAN members", []string{"vlan"}, nil),
		vlanMemberInfo: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "member_info"),
			"Non-numeric data about VLAN member, value is always 1", []string{"vlan", "member", "tagging_mode"}, nil),
		scrapeDuration: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "scrape_duration_seconds"),
			"Time it took for exporter to refresh VLAN metrics", nil, nil),
		scrapeCollectorSuccess: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "collector_success"),
			"Whether VLAN collector succeeded", nil, nil),
		cacheAge: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "cache_age_seconds"),
			"Age of latest VLAN cache refresh", nil, nil),
		skippedEntries: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries_skipped"),
			"Number of VLAN entries skipped during latest refresh", nil, nil),
		logger: logger,
		config: loadVlanCollectorConfig(logger),
	}

	if !collector.config.enabled {
		collector.logger.Info("VLAN collector is disabled")
		return collector
	}

	collector.refreshMetrics()
	go collector.refreshLoop()

	return collector
}

func (collector *vlanCollector) IsEnabled() bool {
	return collector.config.enabled
}

func (collector *vlanCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- collector.vlanInfo
	ch <- collector.vlanAdminStatus
	ch <- collector.vlanOperStatus
	ch <- collector.vlanMembers
	ch <- collector.vlanMemberInfo
	ch <- collector.scrapeDuration
	ch <- collector.scrapeCollectorSuccess
	ch <- collector.cacheAge
	ch <- collector.skippedEntries
}

func (collector *vlanCollector) Collect(ch chan<- prometheus.Metric) {
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

func (collector *vlanCollector) refreshLoop() {
	ticker := time.NewTicker(collector.config.refreshInterval)
	defer ticker.Stop()

	for range ticker.C {
		collector.refreshMetrics()
	}
}

func (collector *vlanCollector) refreshMetrics() {
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
		collector.logger.Error("Error refreshing VLAN metrics", "error", err)
		return
	}

	collector.cachedMetrics = metrics
	collector.lastSkippedEntries = float64(skippedEntries)
	collector.lastSuccess = 1
	collector.lastRefreshTime = time.Now()
}

func (collector *vlanCollector) scrapeMetrics(ctx context.Context) ([]prometheus.Metric, int, error) {
	redisClient, err := redis.NewClient()
	if err != nil {
		return nil, 0, fmt.Errorf("redis client initialization failed: %w", err)
	}
	defer redisClient.Close()

	configVlans, err := redisClient.ScanKeysFromDb(ctx, "CONFIG_DB", "VLAN|*", collector.config.redisScanCount)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to scan VLAN keys: %w", err)
	}

	applVlans, err := redisClient.ScanKeysFromDb(ctx, "APPL_DB", "VLAN_TABLE:*", collector.config.redisScanCount)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to scan VLAN_TABLE keys: %w", err)
	}

	memberKeys, err := redisClient.ScanKeysFromDb(ctx, "CONFIG_DB", "VLAN_MEMBER|*", collector.config.redisScanCount)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to scan VLAN_MEMBER keys: %w", err)
	}

	vlans := map[string]struct{}{}
	skippedEntries := 0

	for _, key := range configVlans {
		vlanName := strings.TrimPrefix(key, "VLAN|")
		if vlanName == "" || vlanName == key {
			skippedEntries++
			continue
		}
		vlans[vlanName] = struct{}{}
	}

	for _, key := range applVlans {
		vlanName := strings.TrimPrefix(key, "VLAN_TABLE:")
		if vlanName == "" || vlanName == key {
			skippedEntries++
			continue
		}
		vlans[vlanName] = struct{}{}
	}

	vlanNames := make([]string, 0, len(vlans))
	for vlanName := range vlans {
		vlanNames = append(vlanNames, vlanName)
	}
	sort.Strings(vlanNames)

	membersByVlan := map[string][]vlanMemberEntry{}
	for _, key := range memberKeys {
		memberPath := strings.TrimPrefix(key, "VLAN_MEMBER|")
		if memberPath == "" || memberPath == key {
			skippedEntries++
			continue
		}

		parts := strings.SplitN(memberPath, "|", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			skippedEntries++
			continue
		}

		memberData, err := redisClient.HgetAllFromDb(ctx, "CONFIG_DB", key)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to read %s: %w", key, err)
		}

		taggingMode := firstNonEmpty(memberData["tagging_mode"], "unknown")
		membersByVlan[parts[0]] = append(membersByVlan[parts[0]], vlanMemberEntry{name: parts[1], taggingMode: taggingMode})
	}

	metrics := make([]prometheus.Metric, 0)
	processedVlans := 0
	processedMembers := 0

	for _, vlanName := range vlanNames {
		if processedVlans >= collector.config.maxVlans {
			skippedEntries++
			continue
		}

		configData, err := redisClient.HgetAllFromDb(ctx, "CONFIG_DB", "VLAN|"+vlanName)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to read CONFIG_DB VLAN|%s: %w", vlanName, err)
		}

		applData, err := redisClient.HgetAllFromDb(ctx, "APPL_DB", "VLAN_TABLE:"+vlanName)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to read APPL_DB VLAN_TABLE:%s: %w", vlanName, err)
		}

		vlanID := firstNonEmpty(configData["vlanid"], strings.TrimPrefix(vlanName, "Vlan"))
		metrics = append(metrics, prometheus.MustNewConstMetric(collector.vlanInfo, prometheus.GaugeValue, 1, vlanName, vlanID))

		if adminStatus := firstNonEmpty(applData["admin_status"], configData["admin_status"]); adminStatus != "" {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.vlanAdminStatus, prometheus.GaugeValue, statusToGauge(adminStatus), vlanName))
		}

		if operStatus := applData["oper_status"]; operStatus != "" {
			metrics = append(metrics, prometheus.MustNewConstMetric(collector.vlanOperStatus, prometheus.GaugeValue, statusToGauge(operStatus), vlanName))
		}

		members := membersByVlan[vlanName]
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
				collector.vlanMemberInfo,
				prometheus.GaugeValue,
				1,
				vlanName,
				member.name,
				member.taggingMode,
			))

			processedMembers++
			memberCount++
		}

		metrics = append(metrics, prometheus.MustNewConstMetric(collector.vlanMembers, prometheus.GaugeValue, float64(memberCount), vlanName))
		processedVlans++
	}

	return metrics, skippedEntries, nil
}

func loadVlanCollectorConfig(logger *slog.Logger) vlanCollectorConfig {
	return vlanCollectorConfig{
		enabled:         parseBoolEnv(logger, "VLAN_ENABLED", true),
		refreshInterval: parseDurationEnv(logger, "VLAN_REFRESH_INTERVAL", 30*time.Second),
		timeout:         parseDurationEnv(logger, "VLAN_TIMEOUT", 2*time.Second),
		maxVlans:        parseIntEnv(logger, "VLAN_MAX_VLANS", 1024),
		maxMembers:      parseIntEnv(logger, "VLAN_MAX_MEMBERS", 8192),
		redisScanCount:  256,
	}
}
