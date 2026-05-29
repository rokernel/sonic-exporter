package collector

import (
	"log/slog"
	"os"
	"path"
	"strings"
)

// MetricFilter disables metrics based on SONIC_DISABLED_METRICS.
type MetricFilter struct {
	logger   *slog.Logger
	exact    map[string]struct{}
	patterns []string
}

// NewMetricFilter builds a MetricFilter from SONIC_DISABLED_METRICS.
func NewMetricFilter(logger *slog.Logger) MetricFilter {
	if logger == nil {
		logger = slog.Default()
	}

	filter := MetricFilter{
		logger: logger,
		exact:  make(map[string]struct{}),
	}

	value, exists := os.LookupEnv("SONIC_DISABLED_METRICS")
	if !exists || strings.TrimSpace(value) == "" {
		return filter
	}

	for _, token := range strings.Split(value, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}

		if isGlobPattern(token) {
			if _, err := path.Match(token, ""); err != nil {
				logger.Warn("Ignoring invalid disabled metric pattern", "key", "SONIC_DISABLED_METRICS", "pattern", token, "error", err)
				continue
			}

			filter.patterns = append(filter.patterns, token)
			continue
		}

		filter.exact[token] = struct{}{}
	}

	return filter
}

// Disabled reports whether metricName is disabled.
func (filter MetricFilter) Disabled(metricName string) bool {
	if _, ok := filter.exact[metricName]; ok {
		return true
	}

	for _, pattern := range filter.patterns {
		matched, err := path.Match(pattern, metricName)
		if err != nil {
			continue
		}
		if matched {
			return true
		}
	}

	return false
}

// Enabled reports whether metricName is enabled.
func (filter MetricFilter) Enabled(metricName string) bool {
	return !filter.Disabled(metricName)
}

func isGlobPattern(value string) bool {
	return strings.ContainsAny(value, "*?[")
}
