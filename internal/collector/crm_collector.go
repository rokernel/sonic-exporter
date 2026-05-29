package collector

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/vinted/sonic-exporter/pkg/redis"
)

type crmCollector struct {
	crmResourceAvailable    *prometheus.Desc
	crmResourceUsed         *prometheus.Desc
	crmAclResourceAvailable *prometheus.Desc
	crmAclResourceUsed      *prometheus.Desc
	scrapeDuration          *prometheus.Desc
	scrapeCollectorSuccess  *prometheus.Desc
	cachedMetrics           []prometheus.Metric
	lastScrapeTime          time.Time
	logger                  *slog.Logger
	metricFilter            MetricFilter
	mu                      sync.Mutex
}

const (
	crmResourceAvailableMetricName    = "sonic_crm_resource_available"
	crmResourceUsedMetricName         = "sonic_crm_resource_used"
	crmAclResourceAvailableMetricName = "sonic_crm_acl_resource_available"
	crmAclResourceUsedMetricName      = "sonic_crm_acl_resource_used"
	crmScrapeDurationMetricName       = "sonic_crm_scrape_duration_seconds"
	crmCollectorSuccessMetricName     = "sonic_crm_collector_success"
)

func NewCrmCollector(logger *slog.Logger, metricFilter MetricFilter) *crmCollector {
	const (
		namespace = "sonic"
		subsystem = "crm"
	)

	return &crmCollector{
		crmResourceAvailable: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "resource_available"),
			"Maximum available value for a resource", []string{"resource"}, nil),
		crmResourceUsed: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "resource_used"),
			"Used value for a resource", []string{"resource"}, nil),
		crmAclResourceAvailable: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "acl_resource_available"),
			"Maximum available value for an ACL resource", []string{"acl_target", "resource"}, nil),
		crmAclResourceUsed: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "acl_resource_used"),
			"Used value for an ACL resource", []string{"acl_target", "resource"}, nil),
		scrapeDuration: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "scrape_duration_seconds"),
			"Time it took for prometheus to scrape sonic crm metrics", nil, nil),
		scrapeCollectorSuccess: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "collector_success"),
			"Whether crm collector succeeded", nil, nil),
		logger:       logger,
		metricFilter: metricFilter,
	}
}

func (collector *crmCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- collector.crmResourceAvailable
	ch <- collector.crmResourceUsed
	ch <- collector.crmAclResourceAvailable
	ch <- collector.crmAclResourceUsed
	ch <- collector.scrapeDuration
	ch <- collector.scrapeCollectorSuccess
}

func (collector *crmCollector) Collect(ch chan<- prometheus.Metric) {
	const cacheDuration = 15 * time.Second

	scrapeSuccess := 1.0

	var ctx = context.Background()

	collector.mu.Lock()
	defer collector.mu.Unlock()

	if time.Since(collector.lastScrapeTime) < cacheDuration {
		// Return cached metrics without making redis calls
		collector.logger.Info("Returning crm metrics from cache")

		for _, metric := range collector.cachedMetrics {
			ch <- metric
		}
		return
	}

	err := collector.scrapeMetrics(ctx)
	if err != nil {
		scrapeSuccess = 0
		collector.logger.Error("Error scraping metrics", "error", err)
	}
	if collector.metricFilter.Enabled(crmCollectorSuccessMetricName) {
		collector.cachedMetrics = append(collector.cachedMetrics, prometheus.MustNewConstMetric(
			collector.scrapeCollectorSuccess, prometheus.GaugeValue, scrapeSuccess,
		))
	}

	for _, cachedMetric := range collector.cachedMetrics {
		ch <- cachedMetric
	}
}

func (collector *crmCollector) scrapeMetrics(ctx context.Context) error {
	collector.logger.Info("Starting crm metric scrape")
	scrapeTime := time.Now()

	redisClient, err := redis.NewClient()
	if err != nil {
		return fmt.Errorf("redis client initialization failed: %w", err)
	}

	defer redisClient.Close()

	// Reset metrics
	collector.cachedMetrics = []prometheus.Metric{}

	crmStats, err := redisClient.HgetAllFromDb(ctx, "COUNTERS_DB", "CRM:STATS")
	if err != nil {
		return fmt.Errorf("redis read failed: %w", err)
	}

	err = collector.collectCrmStatsCounters(ctx, crmStats)
	if err != nil {
		return fmt.Errorf("crm stats collection failed: %w", err)
	}

	err = collector.collectCrmAclStats(ctx, redisClient)
	if err != nil {
		return fmt.Errorf("crm acl stats collection failed: %w", err)
	}

	collector.logger.Info("Ending crm metric scrape")
	collector.lastScrapeTime = time.Now()
	if collector.metricFilter.Enabled(crmScrapeDurationMetricName) {
		collector.cachedMetrics = append(collector.cachedMetrics, prometheus.MustNewConstMetric(
			collector.scrapeDuration, prometheus.GaugeValue, time.Since(scrapeTime).Seconds(),
		))
	}
	return nil
}

func (collector *crmCollector) collectCrmStatsCounters(ctx context.Context, crmStats map[string]string) error {
	for stat, value := range crmStats {
		parsedValue, err := parseFloat(value)
		if err != nil {
			return fmt.Errorf("value parse failed: %w", err)
		}

		if strings.HasSuffix(stat, "available") {
			label := strings.TrimSuffix(strings.TrimPrefix(stat, "crm_stats_"), "_available")
			if collector.metricFilter.Enabled(crmResourceAvailableMetricName) {
				collector.cachedMetrics = append(collector.cachedMetrics, prometheus.MustNewConstMetric(
					collector.crmResourceAvailable, prometheus.GaugeValue, parsedValue, label,
				))
			}
		}

		if strings.HasSuffix(stat, "used") {
			label := strings.TrimSuffix(strings.TrimPrefix(stat, "crm_stats_"), "_used")
			if collector.metricFilter.Enabled(crmResourceUsedMetricName) {
				collector.cachedMetrics = append(collector.cachedMetrics, prometheus.MustNewConstMetric(
					collector.crmResourceUsed, prometheus.GaugeValue, parsedValue, label,
				))
			}
		}
	}

	return nil
}

func (collector *crmCollector) collectCrmAclStats(ctx context.Context, redisClient redis.Client) error {
	crmAclKeys, err := redisClient.KeysFromDb(ctx, "COUNTERS_DB", "CRM:ACL_STATS:*")
	if err != nil {
		return fmt.Errorf("redis read failed: %w", err)
	}

	for _, key := range crmAclKeys {
		aclTarget := strings.ToLower(strings.Join(strings.Split(key, ":")[2:], "_"))
		aclGroupStats, err := redisClient.HgetAllFromDb(ctx, "COUNTERS_DB", key)
		if err != nil {
			return fmt.Errorf("redis read failed: %w", err)
		}
		for stat, value := range aclGroupStats {
			parsedValue, err := parseFloat(value)
			if err != nil {
				return fmt.Errorf("value parse failed: %w", err)
			}

			if strings.HasSuffix(stat, "available") {
				label := strings.TrimSuffix(strings.TrimPrefix(stat, "crm_stats_"), "_available")
				if collector.metricFilter.Enabled(crmAclResourceAvailableMetricName) {
					collector.cachedMetrics = append(collector.cachedMetrics, prometheus.MustNewConstMetric(
						collector.crmAclResourceAvailable, prometheus.GaugeValue, parsedValue, aclTarget, label,
					))
				}
			}

			if strings.HasSuffix(stat, "used") {
				label := strings.TrimSuffix(strings.TrimPrefix(stat, "crm_stats_"), "_used")
				if collector.metricFilter.Enabled(crmAclResourceUsedMetricName) {
					collector.cachedMetrics = append(collector.cachedMetrics, prometheus.MustNewConstMetric(
						collector.crmAclResourceUsed, prometheus.GaugeValue, parsedValue, aclTarget, label,
					))
				}
			}
		}
	}
	return nil
}
