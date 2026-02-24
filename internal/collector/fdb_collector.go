package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/vinted/sonic-exporter/pkg/redis"
)

const (
	asciiFDBEntryPrefix   = "ASIC_STATE:SAI_OBJECT_TYPE_FDB_ENTRY:"
	asciiVLANObjectPrefix = "ASIC_STATE:SAI_OBJECT_TYPE_VLAN:"
	asciiBridgePortPrefix = "ASIC_STATE:SAI_OBJECT_TYPE_BRIDGE_PORT:"
)

type fdbCollectorConfig struct {
	enabled         bool
	refreshInterval time.Duration
	timeout         time.Duration
	maxEntries      int
	maxPorts        int
	maxVlans        int
	redisScanCount  int64
}

type fdbCollector struct {
	fdbEntries             *prometheus.Desc
	fdbEntriesByPort       *prometheus.Desc
	fdbEntriesByVlan       *prometheus.Desc
	fdbEntriesByType       *prometheus.Desc
	fdbEntriesUnknownVlan  *prometheus.Desc
	fdbEntriesTruncated    *prometheus.Desc
	fdbEntriesSkipped      *prometheus.Desc
	scrapeDuration         *prometheus.Desc
	scrapeCollectorSuccess *prometheus.Desc
	cacheAge               *prometheus.Desc

	logger *slog.Logger
	config fdbCollectorConfig

	mu                 sync.RWMutex
	cachedMetrics      []prometheus.Metric
	lastSuccess        float64
	lastScrapeDuration float64
	lastSkippedEntries float64
	lastTruncated      float64
	lastRefreshTime    time.Time
}

type fdbEntryKey struct {
	Bvid string `json:"bvid"`
	Vlan string `json:"vlan"`
	Mac  string `json:"mac"`
}

func NewFdbCollector(logger *slog.Logger) *fdbCollector {
	const (
		namespace = "sonic"
		subsystem = "fdb"
	)

	collector := &fdbCollector{
		fdbEntries: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries"),
			"Number of FDB entries", nil, nil),
		fdbEntriesByPort: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries_by_port"),
			"Number of FDB entries by port", []string{"port"}, nil),
		fdbEntriesByVlan: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries_by_vlan"),
			"Number of FDB entries by VLAN", []string{"vlan"}, nil),
		fdbEntriesByType: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries_by_type"),
			"Number of FDB entries by entry type", []string{"entry_type"}, nil),
		fdbEntriesUnknownVlan: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries_unknown_vlan"),
			"Number of FDB entries with unknown VLAN mapping", nil, nil),
		fdbEntriesTruncated: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries_truncated"),
			"Whether FDB collection hit max entries limit (1=yes, 0=no)", nil, nil),
		fdbEntriesSkipped: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "entries_skipped"),
			"Number of FDB entries skipped during latest refresh", nil, nil),
		scrapeDuration: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "scrape_duration_seconds"),
			"Time it took for exporter to refresh FDB metrics", nil, nil),
		scrapeCollectorSuccess: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "collector_success"),
			"Whether FDB collector succeeded", nil, nil),
		cacheAge: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "cache_age_seconds"),
			"Age of latest FDB cache refresh", nil, nil),
		logger: logger,
		config: loadFdbCollectorConfig(logger),
	}

	if !collector.config.enabled {
		collector.logger.Info("FDB collector is disabled")
		return collector
	}

	collector.refreshMetrics()
	go collector.refreshLoop()

	return collector
}

func (collector *fdbCollector) IsEnabled() bool {
	return collector.config.enabled
}

func (collector *fdbCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- collector.fdbEntries
	ch <- collector.fdbEntriesByPort
	ch <- collector.fdbEntriesByVlan
	ch <- collector.fdbEntriesByType
	ch <- collector.fdbEntriesUnknownVlan
	ch <- collector.fdbEntriesTruncated
	ch <- collector.fdbEntriesSkipped
	ch <- collector.scrapeDuration
	ch <- collector.scrapeCollectorSuccess
	ch <- collector.cacheAge
}

func (collector *fdbCollector) Collect(ch chan<- prometheus.Metric) {
	if !collector.config.enabled {
		return
	}

	collector.mu.RLock()
	cachedMetrics := append([]prometheus.Metric{}, collector.cachedMetrics...)
	lastSkippedEntries := collector.lastSkippedEntries
	lastTruncated := collector.lastTruncated
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

	ch <- prometheus.MustNewConstMetric(collector.fdbEntriesSkipped, prometheus.GaugeValue, lastSkippedEntries)
	ch <- prometheus.MustNewConstMetric(collector.fdbEntriesTruncated, prometheus.GaugeValue, lastTruncated)
	ch <- prometheus.MustNewConstMetric(collector.scrapeDuration, prometheus.GaugeValue, lastScrapeDuration)
	ch <- prometheus.MustNewConstMetric(collector.scrapeCollectorSuccess, prometheus.GaugeValue, lastSuccess)
	ch <- prometheus.MustNewConstMetric(collector.cacheAge, prometheus.GaugeValue, cacheAge)
}

func (collector *fdbCollector) refreshLoop() {
	ticker := time.NewTicker(collector.config.refreshInterval)
	defer ticker.Stop()

	for range ticker.C {
		collector.refreshMetrics()
	}
}

func (collector *fdbCollector) refreshMetrics() {
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
		collector.logger.Error("Error refreshing FDB metrics", "error", err)
		return
	}

	collector.cachedMetrics = metrics
	collector.lastSkippedEntries = float64(skippedEntries)
	collector.lastTruncated = truncated
	collector.lastSuccess = 1
	collector.lastRefreshTime = time.Now()
}

func (collector *fdbCollector) scrapeMetrics(ctx context.Context) ([]prometheus.Metric, int, float64, error) {
	redisClient, err := redis.NewClient()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("redis client initialization failed: %w", err)
	}
	defer redisClient.Close()

	portOidToName, err := collector.portOidToNameMap(ctx, redisClient)
	if err != nil {
		return nil, 0, 0, err
	}

	bvidToVlan, skippedEntries, err := collector.bvidToVlanMap(ctx, redisClient)
	if err != nil {
		return nil, 0, 0, err
	}

	bridgePortToPort, bridgeSkippedEntries, err := collector.bridgePortToPortMap(ctx, redisClient, portOidToName)
	if err != nil {
		return nil, 0, 0, err
	}
	skippedEntries += bridgeSkippedEntries

	fdbKeys, err := redisClient.ScanKeysFromDb(ctx, "ASIC_DB", asciiFDBEntryPrefix+"*", collector.config.redisScanCount)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to scan FDB keys: %w", err)
	}

	sort.Strings(fdbKeys)

	truncated := 0.0
	entriesTotal := 0.0
	unknownVLANEntries := 0.0
	entriesByVLAN := map[string]float64{}
	entriesByPort := map[string]float64{}
	entriesByType := map[string]float64{}

	for idx, fdbKey := range fdbKeys {
		if idx >= collector.config.maxEntries {
			truncated = 1
			skippedEntries += len(fdbKeys) - idx
			break
		}

		parsedKey, err := parseFDBKey(fdbKey)
		if err != nil {
			skippedEntries++
			continue
		}

		fdbData, err := redisClient.HgetAllFromDb(ctx, "ASIC_DB", fdbKey)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("failed to read FDB entry %s: %w", fdbKey, err)
		}

		if len(fdbData) == 0 {
			skippedEntries++
			continue
		}

		vlanLabel, unknownVLAN := resolveFDBVLANLabel(parsedKey, bvidToVlan)
		if unknownVLAN {
			unknownVLANEntries++
		}

		entryType := normalizeFDBType(fdbData["SAI_FDB_ENTRY_ATTR_TYPE"])
		if entryType == "" {
			entryType = "unknown"
		}

		portLabel := bridgePortToPort[fdbData["SAI_FDB_ENTRY_ATTR_BRIDGE_PORT_ID"]]
		if portLabel == "" {
			portLabel = "unknown"
		}

		entriesTotal++
		entriesByVLAN[vlanLabel]++
		entriesByPort[portLabel]++
		entriesByType[entryType]++
	}

	metrics := make([]prometheus.Metric, 0, len(entriesByVLAN)+len(entriesByPort)+len(entriesByType)+2)
	metrics = append(metrics, prometheus.MustNewConstMetric(collector.fdbEntries, prometheus.GaugeValue, entriesTotal))
	metrics = append(metrics, prometheus.MustNewConstMetric(collector.fdbEntriesUnknownVlan, prometheus.GaugeValue, unknownVLANEntries))

	vlanNames := sortedMapKeys(entriesByVLAN)
	for idx, vlanName := range vlanNames {
		if idx >= collector.config.maxVlans {
			skippedEntries += len(vlanNames) - idx
			break
		}
		metrics = append(metrics, prometheus.MustNewConstMetric(collector.fdbEntriesByVlan, prometheus.GaugeValue, entriesByVLAN[vlanName], vlanName))
	}

	portNames := sortedMapKeys(entriesByPort)
	for idx, portName := range portNames {
		if idx >= collector.config.maxPorts {
			skippedEntries += len(portNames) - idx
			break
		}
		metrics = append(metrics, prometheus.MustNewConstMetric(collector.fdbEntriesByPort, prometheus.GaugeValue, entriesByPort[portName], portName))
	}

	entryTypes := sortedMapKeys(entriesByType)
	for _, entryType := range entryTypes {
		metrics = append(metrics, prometheus.MustNewConstMetric(collector.fdbEntriesByType, prometheus.GaugeValue, entriesByType[entryType], entryType))
	}

	return metrics, skippedEntries, truncated, nil
}

func (collector *fdbCollector) portOidToNameMap(ctx context.Context, redisClient redis.Client) (map[string]string, error) {
	portNameMap, err := redisClient.HgetAllFromDb(ctx, "COUNTERS_DB", "COUNTERS_PORT_NAME_MAP")
	if err != nil {
		return nil, fmt.Errorf("failed to read COUNTERS_PORT_NAME_MAP: %w", err)
	}

	lagNameMap, err := redisClient.HgetAllFromDb(ctx, "COUNTERS_DB", "COUNTERS_LAG_NAME_MAP")
	if err != nil {
		return nil, fmt.Errorf("failed to read COUNTERS_LAG_NAME_MAP: %w", err)
	}

	oidToName := make(map[string]string, len(portNameMap)+len(lagNameMap))
	for name, oid := range portNameMap {
		oidToName[oid] = name
	}

	for name, oid := range lagNameMap {
		oidToName[oid] = name
	}

	return oidToName, nil
}

func (collector *fdbCollector) bvidToVlanMap(ctx context.Context, redisClient redis.Client) (map[string]string, int, error) {
	vlanKeys, err := redisClient.ScanKeysFromDb(ctx, "ASIC_DB", asciiVLANObjectPrefix+"*", collector.config.redisScanCount)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to scan VLAN object keys: %w", err)
	}

	bvidToVlan := make(map[string]string, len(vlanKeys))
	skippedEntries := 0

	for _, vlanKey := range vlanKeys {
		bvid := strings.TrimPrefix(vlanKey, asciiVLANObjectPrefix)
		if bvid == "" || bvid == vlanKey {
			skippedEntries++
			continue
		}

		vlanData, err := redisClient.HgetAllFromDb(ctx, "ASIC_DB", vlanKey)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to read VLAN object %s: %w", vlanKey, err)
		}

		vlanID := strings.TrimSpace(vlanData["SAI_VLAN_ATTR_VLAN_ID"])
		if vlanID == "" {
			skippedEntries++
			continue
		}

		bvidToVlan[bvid] = vlanID
	}

	return bvidToVlan, skippedEntries, nil
}

func (collector *fdbCollector) bridgePortToPortMap(ctx context.Context, redisClient redis.Client, portOidToName map[string]string) (map[string]string, int, error) {
	bridgePortKeys, err := redisClient.ScanKeysFromDb(ctx, "ASIC_DB", asciiBridgePortPrefix+"*", collector.config.redisScanCount)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to scan bridge port keys: %w", err)
	}

	bridgePortToPort := make(map[string]string, len(bridgePortKeys))
	skippedEntries := 0

	for _, bridgePortKey := range bridgePortKeys {
		bridgePortID := strings.TrimPrefix(bridgePortKey, asciiBridgePortPrefix)
		if bridgePortID == "" || bridgePortID == bridgePortKey {
			skippedEntries++
			continue
		}

		bridgePortData, err := redisClient.HgetAllFromDb(ctx, "ASIC_DB", bridgePortKey)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to read bridge port %s: %w", bridgePortKey, err)
		}

		portID := bridgePortData["SAI_BRIDGE_PORT_ATTR_PORT_ID"]
		if portID == "" {
			continue
		}

		portName := portOidToName[portID]
		if portName == "" {
			skippedEntries++
			continue
		}

		bridgePortToPort[bridgePortID] = portName
	}

	return bridgePortToPort, skippedEntries, nil
}

func loadFdbCollectorConfig(logger *slog.Logger) fdbCollectorConfig {
	return fdbCollectorConfig{
		enabled:         parseBoolEnv(logger, "FDB_ENABLED", false),
		refreshInterval: parseDurationEnv(logger, "FDB_REFRESH_INTERVAL", 60*time.Second),
		timeout:         parseDurationEnv(logger, "FDB_TIMEOUT", 2*time.Second),
		maxEntries:      parseIntEnv(logger, "FDB_MAX_ENTRIES", 50000),
		maxPorts:        parseIntEnv(logger, "FDB_MAX_PORTS", 1024),
		maxVlans:        parseIntEnv(logger, "FDB_MAX_VLANS", 4096),
		redisScanCount:  256,
	}
}

func parseFDBKey(fdbKey string) (fdbEntryKey, error) {
	entryKey := fdbEntryKey{}

	payload := strings.TrimPrefix(fdbKey, asciiFDBEntryPrefix)
	if payload == "" || payload == fdbKey {
		return entryKey, fmt.Errorf("fdb key has invalid format")
	}

	if err := json.Unmarshal([]byte(payload), &entryKey); err != nil {
		return entryKey, err
	}

	if entryKey.Mac == "" {
		return entryKey, fmt.Errorf("missing mac in fdb key")
	}

	return entryKey, nil
}

func resolveFDBVLANLabel(entryKey fdbEntryKey, bvidToVlan map[string]string) (string, bool) {
	vlanLabel := strings.TrimSpace(entryKey.Vlan)
	if vlanLabel != "" {
		return vlanLabel, false
	}

	vlanLabel = bvidToVlan[strings.TrimSpace(entryKey.Bvid)]
	if vlanLabel != "" {
		return vlanLabel, false
	}

	return "unknown", true
}

func normalizeFDBType(rawValue string) string {
	entryType := strings.TrimSpace(strings.ToLower(rawValue))
	entryType = strings.TrimPrefix(entryType, "sai_fdb_entry_type_")
	return entryType
}

func sortedMapKeys(values map[string]float64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
