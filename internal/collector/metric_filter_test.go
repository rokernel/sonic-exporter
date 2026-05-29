package collector

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

type metricExpectation struct {
	metric   string
	disabled bool
}

func TestMetricFilter(t *testing.T) {
	tests := []struct {
		name            string
		setEnv          bool
		envValue        string
		wantWarn        bool
		expectations    []metricExpectation
		warnMustContain []string
	}{
		{
			name:   "unset env keeps all enabled",
			setEnv: false,
			expectations: []metricExpectation{
				{metric: metricName("queue", "watermark_bytes_total"), disabled: false},
			},
		},
		{
			name:     "empty env keeps all enabled",
			setEnv:   true,
			envValue: "",
			expectations: []metricExpectation{
				{metric: metricName("queue", "watermark_bytes_total"), disabled: false},
			},
		},
		{
			name:     "whitespace env keeps all enabled",
			setEnv:   true,
			envValue: "  \t  ",
			expectations: []metricExpectation{
				{metric: metricName("queue", "watermark_bytes_total"), disabled: false},
			},
		},
		{
			name:     "exact match disables full metric name",
			setEnv:   true,
			envValue: "sonic_queue_watermark_bytes_total",
			expectations: []metricExpectation{
				{metric: metricName("queue", "watermark_bytes_total"), disabled: true},
			},
		},
		{
			name:     "exact non-match leaves other metrics enabled",
			setEnv:   true,
			envValue: "sonic_queue_watermark_bytes_total",
			expectations: []metricExpectation{
				{metric: metricName("queue", "queue_depth_bytes"), disabled: false},
			},
		},
		{
			name:     "wildcard match disables queue metrics",
			setEnv:   true,
			envValue: "sonic_queue_*",
			expectations: []metricExpectation{
				{metric: metricName("queue", "watermark_bytes_total"), disabled: true},
			},
		},
		{
			name:     "wildcard non-match leaves other metrics enabled",
			setEnv:   true,
			envValue: "sonic_queue_*",
			expectations: []metricExpectation{
				{metric: metricName("interface", "operational_status"), disabled: false},
			},
		},
		{
			name:     "comma list with spaces matches both forms",
			setEnv:   true,
			envValue: " sonic_queue_* , sonic_lldp_neighbors ",
			expectations: []metricExpectation{
				{metric: metricName("queue", "watermark_bytes_total"), disabled: true},
				{metric: "sonic_lldp_neighbors", disabled: true},
				{metric: metricName("interface", "operational_status"), disabled: false},
			},
		},
		{
			name:     "duplicate entries still disable matching metrics",
			setEnv:   true,
			envValue: "sonic_queue_*, sonic_queue_*",
			expectations: []metricExpectation{
				{metric: metricName("queue", "watermark_bytes_total"), disabled: true},
			},
		},
		{
			name:     "invalid wildcard is ignored",
			setEnv:   true,
			envValue: "sonic_queue_[",
			wantWarn: true,
			expectations: []metricExpectation{
				{metric: metricName("queue", "watermark_bytes_total"), disabled: false},
			},
			warnMustContain: []string{"SONIC_DISABLED_METRICS", "sonic_queue_["},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configureMetricFilterEnv(t, tt.setEnv, tt.envValue)

			logger, buffer := newMetricFilterLogger()
			filter := NewMetricFilter(logger)

			for _, expectation := range tt.expectations {
				if got := filter.Disabled(expectation.metric); got != expectation.disabled {
					t.Fatalf("Disabled(%q) = %v, want %v", expectation.metric, got, expectation.disabled)
				}
				if got := filter.Enabled(expectation.metric); got != !expectation.disabled {
					t.Fatalf("Enabled(%q) = %v, want %v", expectation.metric, got, !expectation.disabled)
				}
			}

			if tt.wantWarn {
				logOutput := buffer.String()
				for _, fragment := range tt.warnMustContain {
					if !strings.Contains(logOutput, fragment) {
						t.Fatalf("warning log %q does not contain %q", logOutput, fragment)
					}
				}
			}
		})
	}
}

func configureMetricFilterEnv(t *testing.T, setEnv bool, value string) {
	t.Helper()

	const key = "SONIC_DISABLED_METRICS"
	if setEnv {
		t.Setenv(key, value)
		return
	}

	previousValue, hadValue := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}

	t.Cleanup(func() {
		if hadValue {
			_ = os.Setenv(key, previousValue)
			return
		}
		_ = os.Unsetenv(key)
	})
}

func newMetricFilterLogger() (*slog.Logger, *bytes.Buffer) {
	var buffer bytes.Buffer
	handler := slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelWarn})
	return slog.New(handler), &buffer
}

func metricName(subsystem, metric string) string {
	return prometheus.BuildFQName("sonic", subsystem, metric)
}
