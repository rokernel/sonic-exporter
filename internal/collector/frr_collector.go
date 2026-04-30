package collector

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
	frrcollector "github.com/tynany/frr_exporter/collector"
)

type frrCollectorConfig struct {
	enabled                            bool
	socketDirPath                      string
	socketTimeout                      time.Duration
	vtyshEnabled                       bool
	vtyshPath                          string
	vtyshTimeout                       time.Duration
	vtyshSudo                          bool
	vtyshOptions                       string
	bgpEnabled                         bool
	bgp6Enabled                        bool
	bgpL2VPNEnabled                    bool
	ospfEnabled                        bool
	ospfInstances                      string
	bfdEnabled                         bool
	routeEnabled                       bool
	routeDetailedEnabled               bool
	rpkiEnabled                        bool
	vrrpEnabled                        bool
	pimEnabled                         bool
	statusEnabled                      bool
	bgpPeerTypesEnabled                bool
	bgpPeerTypesKeys                   string
	bgpPeerDescriptionsEnabled         bool
	bgpPeerDescriptionsPlainText       bool
	bgpPeerGroupsEnabled               bool
	bgpPeerHostnamesEnabled            bool
	bgpAdvertisedPrefixesEnabled       bool
	bgpAcceptedFilteredPrefixesEnabled bool
	bgpNextHopInterfaceEnabled         bool
	bgpMonitoredPrefixesFile           string
}

type frrCollector struct {
	logger   *slog.Logger
	config   frrCollectorConfig
	exporter *frrcollector.Exporter
}

func NewFrrCollector(logger *slog.Logger) *frrCollector {
	collector := &frrCollector{
		logger: logger,
		config: loadFrrCollectorConfig(logger),
	}

	if !collector.config.enabled {
		collector.logger.Info("FRR collector is disabled")
		return collector
	}

	args := buildFrrCollectorArgs(collector.config)
	if _, err := kingpin.CommandLine.Parse(args); err != nil {
		collector.logger.Error("Failed to parse FRR exporter flags", "error", err)
		collector.config.enabled = false
		return collector
	}

	exporter, err := frrcollector.NewExporter(logger.With("collector", "frr"))
	if err != nil {
		collector.logger.Error("Failed to create FRR exporter", "error", err)
		collector.config.enabled = false
		return collector
	}

	collector.exporter = exporter
	return collector
}

func (collector *frrCollector) IsEnabled() bool {
	return collector.config.enabled && collector.exporter != nil
}

func (collector *frrCollector) Describe(ch chan<- *prometheus.Desc) {
	if !collector.IsEnabled() {
		return
	}

	collector.exporter.Describe(ch)
}

func (collector *frrCollector) Collect(ch chan<- prometheus.Metric) {
	if !collector.IsEnabled() {
		return
	}

	collector.exporter.Collect(ch)
}

func loadFrrCollectorConfig(logger *slog.Logger) frrCollectorConfig {
	return frrCollectorConfig{
		enabled:                            parseBoolEnv(logger, "FRR_ENABLED", false),
		socketDirPath:                      parseStringEnv("FRR_SOCKET_DIR_PATH", "/var/run/frr"),
		socketTimeout:                      parseDurationEnv(logger, "FRR_SOCKET_TIMEOUT", 20*time.Second),
		vtyshEnabled:                       parseBoolEnv(logger, "FRR_VTYSH_ENABLED", false),
		vtyshPath:                          parseStringEnv("FRR_VTYSH_PATH", "/usr/bin/vtysh"),
		vtyshTimeout:                       parseDurationEnv(logger, "FRR_VTYSH_TIMEOUT", 20*time.Second),
		vtyshSudo:                          parseBoolEnv(logger, "FRR_VTYSH_SUDO", false),
		vtyshOptions:                       parseStringEnv("FRR_VTYSH_OPTIONS", ""),
		bgpEnabled:                         parseBoolEnv(logger, "FRR_BGP_ENABLED", true),
		bgp6Enabled:                        parseBoolEnv(logger, "FRR_BGP6_ENABLED", false),
		bgpL2VPNEnabled:                    parseBoolEnv(logger, "FRR_BGPL2VPN_ENABLED", false),
		ospfEnabled:                        parseBoolEnv(logger, "FRR_OSPF_ENABLED", true),
		ospfInstances:                      parseStringEnv("FRR_OSPF_INSTANCES", ""),
		bfdEnabled:                         parseBoolEnv(logger, "FRR_BFD_ENABLED", true),
		routeEnabled:                       parseBoolEnv(logger, "FRR_ROUTE_ENABLED", true),
		routeDetailedEnabled:               parseBoolEnv(logger, "FRR_ROUTE_DETAILED_ENABLED", false),
		rpkiEnabled:                        parseBoolEnv(logger, "FRR_RPKI_ENABLED", false),
		vrrpEnabled:                        parseBoolEnv(logger, "FRR_VRRP_ENABLED", false),
		pimEnabled:                         parseBoolEnv(logger, "FRR_PIM_ENABLED", false),
		statusEnabled:                      parseBoolEnv(logger, "FRR_STATUS_ENABLED", true),
		bgpPeerTypesEnabled:                parseBoolEnv(logger, "FRR_BGP_PEER_TYPES_ENABLED", false),
		bgpPeerTypesKeys:                   parseStringEnv("FRR_BGP_PEER_TYPES_KEYS", "type"),
		bgpPeerDescriptionsEnabled:         parseBoolEnv(logger, "FRR_BGP_PEER_DESCRIPTIONS_ENABLED", false),
		bgpPeerDescriptionsPlainText:       parseBoolEnv(logger, "FRR_BGP_PEER_DESCRIPTIONS_PLAIN_TEXT", false),
		bgpPeerGroupsEnabled:               parseBoolEnv(logger, "FRR_BGP_PEER_GROUPS_ENABLED", false),
		bgpPeerHostnamesEnabled:            parseBoolEnv(logger, "FRR_BGP_PEER_HOSTNAMES_ENABLED", false),
		bgpAdvertisedPrefixesEnabled:       parseBoolEnv(logger, "FRR_BGP_ADVERTISED_PREFIXES_ENABLED", false),
		bgpAcceptedFilteredPrefixesEnabled: parseBoolEnv(logger, "FRR_BGP_ACCEPTED_FILTERED_PREFIXES_ENABLED", false),
		bgpNextHopInterfaceEnabled:         parseBoolEnv(logger, "FRR_BGP_NEXT_HOP_INTERFACE_ENABLED", false),
		bgpMonitoredPrefixesFile:           parseStringEnv("FRR_BGP_MONITORED_PREFIXES_FILE", ""),
	}
}

func buildFrrCollectorArgs(config frrCollectorConfig) []string {
	args := []string{}

	args = appendStringFlag(args, "frr.socket.dir-path", config.socketDirPath, "/var/run/frr")
	args = appendDurationFlag(args, "frr.socket.timeout", config.socketTimeout, 20*time.Second)
	args = appendBoolFlag(args, "frr.vtysh", config.vtyshEnabled, false)
	args = appendStringFlag(args, "frr.vtysh.path", config.vtyshPath, "/usr/bin/vtysh")
	args = appendDurationFlag(args, "frr.vtysh.timeout", config.vtyshTimeout, 20*time.Second)
	args = appendBoolFlag(args, "frr.vtysh.sudo", config.vtyshSudo, false)
	args = appendStringFlag(args, "frr.vtysh.options", config.vtyshOptions, "")

	args = appendBoolFlag(args, "collector.bgp", config.bgpEnabled, true)
	args = appendBoolFlag(args, "collector.bgp6", config.bgp6Enabled, false)
	args = appendBoolFlag(args, "collector.bgpl2vpn", config.bgpL2VPNEnabled, false)
	args = appendBoolFlag(args, "collector.ospf", config.ospfEnabled, true)
	args = appendStringFlag(args, "collector.ospf.instances", config.ospfInstances, "")
	args = appendBoolFlag(args, "collector.bfd", config.bfdEnabled, true)
	args = appendBoolFlag(args, "collector.route", config.routeEnabled, true)
	args = appendBoolFlag(args, "collector.route.detailed-routes", config.routeDetailedEnabled, false)
	args = appendBoolFlag(args, "collector.rpki", config.rpkiEnabled, false)
	args = appendBoolFlag(args, "collector.vrrp", config.vrrpEnabled, false)
	args = appendBoolFlag(args, "collector.pim", config.pimEnabled, false)
	args = appendBoolFlag(args, "collector.status", config.statusEnabled, true)

	args = appendBoolFlag(args, "collector.bgp.peer-types", config.bgpPeerTypesEnabled, false)
	args = appendRepeatedStringFlag(args, "collector.bgp.peer-types.keys", splitCommaSeparated(config.bgpPeerTypesKeys), []string{"type"})
	args = appendBoolFlag(args, "collector.bgp.peer-descriptions", config.bgpPeerDescriptionsEnabled, false)
	args = appendBoolFlag(args, "collector.bgp.peer-descriptions.plain-text", config.bgpPeerDescriptionsPlainText, false)
	args = appendBoolFlag(args, "collector.bgp.peer-groups", config.bgpPeerGroupsEnabled, false)
	args = appendBoolFlag(args, "collector.bgp.peer-hostnames", config.bgpPeerHostnamesEnabled, false)
	args = appendBoolFlag(args, "collector.bgp.advertised-prefixes", config.bgpAdvertisedPrefixesEnabled, false)
	args = appendBoolFlag(args, "collector.bgp.accepted-filtered-prefixes", config.bgpAcceptedFilteredPrefixesEnabled, false)
	args = appendBoolFlag(args, "collector.bgp.next-hop-interface", config.bgpNextHopInterfaceEnabled, false)
	args = appendStringFlag(args, "collector.bgp.monitored-prefixes", config.bgpMonitoredPrefixesFile, "")

	return args
}

func appendBoolFlag(args []string, name string, value bool, defaultValue bool) []string {
	if value == defaultValue {
		return args
	}

	if value {
		return append(args, "--"+name)
	}

	return append(args, "--no-"+name)
}

func appendStringFlag(args []string, name string, value string, defaultValue string) []string {
	if value == defaultValue || value == "" {
		return args
	}

	return append(args, fmt.Sprintf("--%s=%s", name, value))
}

func appendDurationFlag(args []string, name string, value time.Duration, defaultValue time.Duration) []string {
	if value == defaultValue {
		return args
	}

	return append(args, fmt.Sprintf("--%s=%s", name, value.String()))
}

func appendRepeatedStringFlag(args []string, name string, values []string, defaultValues []string) []string {
	if strings.Join(values, ",") == strings.Join(defaultValues, ",") {
		return args
	}

	for _, value := range values {
		if value == "" {
			continue
		}
		args = append(args, fmt.Sprintf("--%s=%s", name, value))
	}

	return args
}

func splitCommaSeparated(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}

	parts := strings.Split(value, ",")
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}

	return cleaned
}
