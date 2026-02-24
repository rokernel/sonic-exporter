package collector

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/vinted/sonic-exporter/pkg/redis"
)

const unknownSystemValue = "unknown"

var (
	serialEEPROMRe        = regexp.MustCompile(`^\s*Serial Number\s+0x[0-9A-Fa-f]+\s+\d+\s+(.+)$`)
	productEEPROMRe       = regexp.MustCompile(`^\s*Product Name\s+0x[0-9A-Fa-f]+\s+\d+\s+(.+)$`)
	revisionEEPROMRe      = regexp.MustCompile(`^\s*Label Revision\s+0x[0-9A-Fa-f]+\s+\d+\s+(.+)$`)
	deviceVersionEEPROMRe = regexp.MustCompile(`^\s*Device Version\s+0x[0-9A-Fa-f]+\s+\d+\s+(.+)$`)
)

type systemCollectorConfig struct {
	enabled               bool
	refreshInterval       time.Duration
	timeout               time.Duration
	commandEnabled        bool
	commandTimeout        time.Duration
	commandMaxOutputBytes int
	versionFilePath       string
	machineConfFilePath   string
	hostnameFilePath      string
	uptimeFilePath        string
}

type systemMetadata struct {
	hostname      string
	platform      string
	hwsku         string
	asic          string
	asicCount     string
	serial        string
	model         string
	revision      string
	sonicVersion  string
	sonicOS       string
	debianVersion string
	kernelVersion string
	buildCommit   string
	buildDate     string
	builtBy       string
	branch        string
	release       string
	uptimeSeconds float64
}

type systemCollector struct {
	identityInfo           *prometheus.Desc
	softwareInfo           *prometheus.Desc
	uptimeSeconds          *prometheus.Desc
	scrapeDuration         *prometheus.Desc
	scrapeCollectorSuccess *prometheus.Desc
	cacheAge               *prometheus.Desc

	logger *slog.Logger
	config systemCollectorConfig

	mu                 sync.RWMutex
	cachedMetrics      []prometheus.Metric
	lastSuccess        float64
	lastScrapeDuration float64
	lastRefreshTime    time.Time
}

type limitedOutputBuffer struct {
	buf       bytes.Buffer
	maxBytes  int
	truncated bool
}

func (b *limitedOutputBuffer) Write(p []byte) (int, error) {
	if b.maxBytes <= 0 {
		return len(p), nil
	}

	if b.buf.Len() >= b.maxBytes {
		b.truncated = true
		return len(p), nil
	}

	remaining := b.maxBytes - b.buf.Len()
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}

	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *limitedOutputBuffer) String() string {
	return b.buf.String()
}

func NewSystemCollector(logger *slog.Logger) *systemCollector {
	const (
		namespace = "sonic"
		subsystem = "system"
	)

	collector := &systemCollector{
		identityInfo: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "identity_info"),
			"Switch identity metadata, value is always 1", []string{"hostname", "platform", "hwsku", "asic", "asic_count", "serial", "model", "revision"}, nil),
		softwareInfo: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "software_info"),
			"Switch software metadata, value is always 1", []string{"sonic_version", "sonic_os_version", "debian_version", "kernel_version", "build_commit", "build_date", "built_by", "branch", "release"}, nil),
		uptimeSeconds: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "uptime_seconds"),
			"Switch uptime in seconds", nil, nil),
		scrapeDuration: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "scrape_duration_seconds"),
			"Time it took for exporter to refresh system metrics", nil, nil),
		scrapeCollectorSuccess: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "collector_success"),
			"Whether system collector succeeded", nil, nil),
		cacheAge: prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, "cache_age_seconds"),
			"Age of latest system cache refresh", nil, nil),
		logger: logger,
		config: loadSystemCollectorConfig(logger),
	}

	if !collector.config.enabled {
		collector.logger.Info("System collector is disabled")
		return collector
	}

	collector.refreshMetrics()
	go collector.refreshLoop()

	return collector
}

func (collector *systemCollector) IsEnabled() bool {
	return collector.config.enabled
}

func (collector *systemCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- collector.identityInfo
	ch <- collector.softwareInfo
	ch <- collector.uptimeSeconds
	ch <- collector.scrapeDuration
	ch <- collector.scrapeCollectorSuccess
	ch <- collector.cacheAge
}

func (collector *systemCollector) Collect(ch chan<- prometheus.Metric) {
	if !collector.config.enabled {
		return
	}

	collector.mu.RLock()
	cachedMetrics := append([]prometheus.Metric{}, collector.cachedMetrics...)
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
}

func (collector *systemCollector) refreshLoop() {
	ticker := time.NewTicker(collector.config.refreshInterval)
	defer ticker.Stop()

	for range ticker.C {
		collector.refreshMetrics()
	}
}

func (collector *systemCollector) refreshMetrics() {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), collector.config.timeout)
	defer cancel()

	metrics, err := collector.scrapeMetrics(ctx)
	scrapeDuration := time.Since(start).Seconds()

	collector.mu.Lock()
	defer collector.mu.Unlock()

	collector.lastScrapeDuration = scrapeDuration

	if err != nil {
		collector.lastSuccess = 0
		collector.logger.Error("Error refreshing system metrics", "error", err)
		return
	}

	collector.cachedMetrics = metrics
	collector.lastSuccess = 1
	collector.lastRefreshTime = time.Now()
}

func (collector *systemCollector) scrapeMetrics(ctx context.Context) ([]prometheus.Metric, error) {
	metadata := systemMetadata{}

	collector.loadFromRedis(ctx, &metadata)
	collector.loadFromVersionFile(&metadata)
	collector.loadFromMachineConf(&metadata)
	collector.loadFromHostnameFile(&metadata)
	collector.loadFromUptimeFile(&metadata)

	if collector.config.commandEnabled {
		collector.loadFromShowPlatformSummary(ctx, &metadata)
		collector.loadFromShowVersion(ctx, &metadata)

		if metadata.serial == "" || metadata.model == "" || metadata.revision == "" {
			collector.loadFromShowPlatformSysEEPROM(ctx, &metadata)
		}
	} else {
		collector.logger.Debug("System command sources are disabled")
	}

	metadata = collector.finalizeMetadata(metadata)

	metrics := []prometheus.Metric{
		prometheus.MustNewConstMetric(
			collector.identityInfo,
			prometheus.GaugeValue,
			1,
			metadata.hostname,
			metadata.platform,
			metadata.hwsku,
			metadata.asic,
			metadata.asicCount,
			metadata.serial,
			metadata.model,
			metadata.revision,
		),
		prometheus.MustNewConstMetric(
			collector.softwareInfo,
			prometheus.GaugeValue,
			1,
			metadata.sonicVersion,
			metadata.sonicOS,
			metadata.debianVersion,
			metadata.kernelVersion,
			metadata.buildCommit,
			metadata.buildDate,
			metadata.builtBy,
			metadata.branch,
			metadata.release,
		),
		prometheus.MustNewConstMetric(collector.uptimeSeconds, prometheus.GaugeValue, metadata.uptimeSeconds),
	}

	return metrics, nil
}

func (collector *systemCollector) loadFromRedis(ctx context.Context, metadata *systemMetadata) {
	redisClient, err := redis.NewClient()
	if err != nil {
		collector.logger.Debug("System redis source unavailable", "error", err)
		return
	}
	defer redisClient.Close()

	deviceMetadata, err := redisClient.HgetAllFromDb(ctx, "CONFIG_DB", "DEVICE_METADATA|localhost")
	if err != nil {
		collector.logger.Debug("System redis device metadata read failed", "error", err)
	} else {
		collector.setIfEmpty("hostname", "redis.device_metadata", &metadata.hostname, deviceMetadata["hostname"])
		collector.setIfEmpty("platform", "redis.device_metadata", &metadata.platform, deviceMetadata["platform"])
		collector.setIfEmpty("hwsku", "redis.device_metadata", &metadata.hwsku, deviceMetadata["hwsku"])
	}

	chassisInfo, err := redisClient.HgetAllFromDb(ctx, "STATE_DB", "CHASSIS_INFO|chassis 1")
	if err != nil {
		collector.logger.Debug("System redis chassis info read failed", "error", err)
	} else {
		collector.setIfEmpty("serial", "redis.chassis_info", &metadata.serial, chassisInfo["serial"])
		collector.setIfEmpty("model", "redis.chassis_info", &metadata.model, chassisInfo["model"])
		collector.setIfEmpty("revision", "redis.chassis_info", &metadata.revision, chassisInfo["revision"])
	}
}

func (collector *systemCollector) loadFromVersionFile(metadata *systemMetadata) {
	values, err := parseSimpleKeyValueFile(collector.config.versionFilePath)
	if err != nil {
		collector.logger.Debug("System version file unavailable", "path", collector.config.versionFilePath, "error", err)
		return
	}

	collector.setIfEmpty("sonic_version", "file.sonic_version", &metadata.sonicVersion, values["build_version"])
	collector.setIfEmpty("sonic_os_version", "file.sonic_version", &metadata.sonicOS, values["sonic_os_version"])
	collector.setIfEmpty("debian_version", "file.sonic_version", &metadata.debianVersion, values["debian_version"])
	collector.setIfEmpty("kernel_version", "file.sonic_version", &metadata.kernelVersion, values["kernel_version"])
	collector.setIfEmpty("asic", "file.sonic_version", &metadata.asic, values["asic_type"])
	collector.setIfEmpty("asic_count", "file.sonic_version", &metadata.asicCount, values["asic_count"])
	collector.setIfEmpty("build_commit", "file.sonic_version", &metadata.buildCommit, values["commit_id"])
	collector.setIfEmpty("build_date", "file.sonic_version", &metadata.buildDate, values["build_date"])
	collector.setIfEmpty("built_by", "file.sonic_version", &metadata.builtBy, values["built_by"])
	collector.setIfEmpty("branch", "file.sonic_version", &metadata.branch, values["branch"])
	collector.setIfEmpty("release", "file.sonic_version", &metadata.release, values["release"])
}

func (collector *systemCollector) loadFromMachineConf(metadata *systemMetadata) {
	values, err := parseSimpleKeyValueFile(collector.config.machineConfFilePath)
	if err != nil {
		collector.logger.Debug("System machine.conf unavailable", "path", collector.config.machineConfFilePath, "error", err)
		return
	}

	collector.setIfEmpty("platform", "file.machine_conf", &metadata.platform, values["onie_platform"])
}

func (collector *systemCollector) loadFromHostnameFile(metadata *systemMetadata) {
	hostname, err := readSingleLineFile(collector.config.hostnameFilePath)
	if err != nil {
		collector.logger.Debug("System hostname file unavailable", "path", collector.config.hostnameFilePath, "error", err)
		return
	}

	collector.setIfEmpty("hostname", "file.hostname", &metadata.hostname, hostname)
}

func (collector *systemCollector) loadFromUptimeFile(metadata *systemMetadata) {
	uptimeRaw, err := readSingleLineFile(collector.config.uptimeFilePath)
	if err != nil {
		collector.logger.Debug("System uptime file unavailable", "path", collector.config.uptimeFilePath, "error", err)
		return
	}

	parts := strings.Fields(uptimeRaw)
	if len(parts) == 0 {
		collector.logger.Debug("System uptime file is empty", "path", collector.config.uptimeFilePath)
		return
	}

	uptime, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		collector.logger.Debug("System uptime parse failed", "value", parts[0], "error", err)
		return
	}

	metadata.uptimeSeconds = uptime
}

func (collector *systemCollector) loadFromShowPlatformSummary(ctx context.Context, metadata *systemMetadata) {
	output, err := collector.runAllowedCommand(ctx, []string{"show", "platform", "summary", "--json"})
	if err != nil {
		collector.logger.Debug("System show platform summary source unavailable", "error", err)
		return
	}

	values, err := parseShowPlatformSummaryOutput(output)
	if err != nil {
		collector.logger.Debug("System show platform summary parse failed", "error", err)
		return
	}

	collector.setIfEmpty("platform", "cmd.show_platform_summary", &metadata.platform, values["platform"])
	collector.setIfEmpty("hwsku", "cmd.show_platform_summary", &metadata.hwsku, values["hwsku"])
	collector.setIfEmpty("asic", "cmd.show_platform_summary", &metadata.asic, values["asic_type"])
	collector.setIfEmpty("asic_count", "cmd.show_platform_summary", &metadata.asicCount, values["asic_count"])
	collector.setIfEmpty("serial", "cmd.show_platform_summary", &metadata.serial, values["serial"])
	collector.setIfEmpty("model", "cmd.show_platform_summary", &metadata.model, values["model"])
	collector.setIfEmpty("revision", "cmd.show_platform_summary", &metadata.revision, values["revision"])
}

func (collector *systemCollector) loadFromShowVersion(ctx context.Context, metadata *systemMetadata) {
	output, err := collector.runAllowedCommand(ctx, []string{"show", "version"})
	if err != nil {
		collector.logger.Debug("System show version source unavailable", "error", err)
		return
	}

	values := parseShowVersionOutput(output)

	collector.setIfEmpty("sonic_version", "cmd.show_version", &metadata.sonicVersion, values["sonic_version"])
	collector.setIfEmpty("sonic_os_version", "cmd.show_version", &metadata.sonicOS, values["sonic_os_version"])
	collector.setIfEmpty("debian_version", "cmd.show_version", &metadata.debianVersion, values["debian_version"])
	collector.setIfEmpty("kernel_version", "cmd.show_version", &metadata.kernelVersion, values["kernel_version"])
	collector.setIfEmpty("build_commit", "cmd.show_version", &metadata.buildCommit, values["build_commit"])
	collector.setIfEmpty("build_date", "cmd.show_version", &metadata.buildDate, values["build_date"])
	collector.setIfEmpty("built_by", "cmd.show_version", &metadata.builtBy, values["built_by"])
	collector.setIfEmpty("platform", "cmd.show_version", &metadata.platform, values["platform"])
	collector.setIfEmpty("hwsku", "cmd.show_version", &metadata.hwsku, values["hwsku"])
	collector.setIfEmpty("asic", "cmd.show_version", &metadata.asic, values["asic"])
	collector.setIfEmpty("asic_count", "cmd.show_version", &metadata.asicCount, values["asic_count"])
	collector.setIfEmpty("serial", "cmd.show_version", &metadata.serial, values["serial"])
	collector.setIfEmpty("model", "cmd.show_version", &metadata.model, values["model"])
	collector.setIfEmpty("revision", "cmd.show_version", &metadata.revision, values["revision"])
}

func (collector *systemCollector) loadFromShowPlatformSysEEPROM(ctx context.Context, metadata *systemMetadata) {
	output, err := collector.runAllowedCommand(ctx, []string{"show", "platform", "syseeprom"})
	if err != nil {
		collector.logger.Debug("System show platform syseeprom source unavailable", "error", err)
		return
	}

	values := parseSysEEPROMOutput(output)
	collector.setIfEmpty("serial", "cmd.show_platform_syseeprom", &metadata.serial, values["serial"])
	collector.setIfEmpty("model", "cmd.show_platform_syseeprom", &metadata.model, values["model"])
	collector.setIfEmpty("revision", "cmd.show_platform_syseeprom", &metadata.revision, values["revision"])
}

func (collector *systemCollector) finalizeMetadata(metadata systemMetadata) systemMetadata {
	metadata.hostname = collector.finalizeField("hostname", metadata.hostname)
	metadata.platform = collector.finalizeField("platform", metadata.platform)
	metadata.hwsku = collector.finalizeField("hwsku", metadata.hwsku)
	metadata.asic = strings.ToLower(collector.finalizeField("asic", metadata.asic))
	metadata.asicCount = collector.finalizeField("asic_count", metadata.asicCount)
	metadata.serial = collector.finalizeField("serial", metadata.serial)
	metadata.model = collector.finalizeField("model", metadata.model)
	metadata.revision = collector.finalizeField("revision", metadata.revision)

	metadata.sonicVersion = collector.finalizeField("sonic_version", metadata.sonicVersion)
	metadata.sonicOS = collector.finalizeField("sonic_os_version", metadata.sonicOS)
	metadata.debianVersion = collector.finalizeField("debian_version", metadata.debianVersion)
	metadata.kernelVersion = collector.finalizeField("kernel_version", metadata.kernelVersion)
	metadata.buildCommit = collector.finalizeField("build_commit", metadata.buildCommit)
	metadata.buildDate = collector.finalizeField("build_date", metadata.buildDate)
	metadata.builtBy = collector.finalizeField("built_by", metadata.builtBy)
	metadata.branch = collector.finalizeField("branch", metadata.branch)
	metadata.release = collector.finalizeField("release", metadata.release)

	if _, err := strconv.Atoi(metadata.asicCount); err != nil {
		collector.logger.Debug("System metadata asic_count invalid, forcing unknown", "value", metadata.asicCount)
		metadata.asicCount = unknownSystemValue
	}

	if metadata.uptimeSeconds < 0 {
		collector.logger.Debug("System metadata uptime is negative, forcing zero", "value", metadata.uptimeSeconds)
		metadata.uptimeSeconds = 0
	}

	return metadata
}

func (collector *systemCollector) finalizeField(fieldName, value string) string {
	normalized := normalizeSystemValue(value)
	if normalized == "" {
		collector.logger.Debug("System metadata field missing, using unknown", "field", fieldName, "expected", true)
		return unknownSystemValue
	}
	return normalized
}

func (collector *systemCollector) setIfEmpty(fieldName, source string, target *string, value string) {
	normalized := normalizeSystemValue(value)
	if normalized == "" {
		collector.logger.Debug("System metadata field missing in source", "field", fieldName, "data_source", source, "expected", true)
		return
	}

	if *target == "" {
		*target = normalized
		collector.logger.Debug("System metadata field populated", "field", fieldName, "data_source", source)
		return
	}

	collector.logger.Debug("System metadata field already populated, fallback ignored", "field", fieldName, "data_source", source)
}

func (collector *systemCollector) runAllowedCommand(parentCtx context.Context, args []string) (string, error) {
	allowlist := map[string]struct{}{
		"show platform summary --json": {},
		"show version":                 {},
		"show platform syseeprom":      {},
	}

	commandString := strings.Join(args, " ")
	if _, ok := allowlist[commandString]; !ok {
		return "", fmt.Errorf("command not allowed: %s", commandString)
	}

	cmdCtx, cancel := context.WithTimeout(parentCtx, collector.config.commandTimeout)
	defer cancel()

	command := exec.CommandContext(cmdCtx, args[0], args[1:]...)
	output := &limitedOutputBuffer{maxBytes: collector.config.commandMaxOutputBytes}
	command.Stdout = output
	command.Stderr = output

	err := command.Run()
	if output.truncated {
		collector.logger.Warn("System command output truncated", "command", commandString, "max_bytes", collector.config.commandMaxOutputBytes)
	}

	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("command timed out: %s", commandString)
		}
		return "", fmt.Errorf("command failed: %s: %w", commandString, err)
	}

	return output.String(), nil
}

func loadSystemCollectorConfig(logger *slog.Logger) systemCollectorConfig {
	return systemCollectorConfig{
		enabled:               parseBoolEnv(logger, "SYSTEM_ENABLED", false),
		refreshInterval:       parseDurationEnv(logger, "SYSTEM_REFRESH_INTERVAL", 60*time.Second),
		timeout:               parseDurationEnv(logger, "SYSTEM_TIMEOUT", 4*time.Second),
		commandEnabled:        parseBoolEnv(logger, "SYSTEM_COMMAND_ENABLED", true),
		commandTimeout:        parseDurationEnv(logger, "SYSTEM_COMMAND_TIMEOUT", 2*time.Second),
		commandMaxOutputBytes: parseIntEnv(logger, "SYSTEM_COMMAND_MAX_OUTPUT_BYTES", 262144),
		versionFilePath:       parseStringEnv("SYSTEM_VERSION_FILE", "/etc/sonic/sonic_version.yml"),
		machineConfFilePath:   parseStringEnv("SYSTEM_MACHINE_CONF_FILE", "/host/machine.conf"),
		hostnameFilePath:      parseStringEnv("SYSTEM_HOSTNAME_FILE", "/etc/hostname"),
		uptimeFilePath:        parseStringEnv("SYSTEM_UPTIME_FILE", "/proc/uptime"),
	}
}

func parseStringEnv(key, defaultValue string) string {
	value, exists := os.LookupEnv(key)
	if !exists || strings.TrimSpace(value) == "" {
		return defaultValue
	}

	return strings.TrimSpace(value)
}

func parseSimpleKeyValueFile(filePath string) (map[string]string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	result := map[string]string{}
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		separator := "="
		if len(parts) != 2 {
			parts = strings.SplitN(line, ":", 2)
			separator = ":"
		}
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if separator == ":" && strings.Contains(value, "#") {
			value = strings.TrimSpace(strings.SplitN(value, "#", 2)[0])
		}

		result[key] = strings.Trim(value, "'\"")
	}

	if err = scanner.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

func parseShowPlatformSummaryOutput(output string) (map[string]string, error) {
	jsonStart := strings.Index(output, "{")
	jsonEnd := strings.LastIndex(output, "}")
	if jsonStart == -1 || jsonEnd == -1 || jsonEnd < jsonStart {
		return nil, fmt.Errorf("show platform summary output does not contain json object")
	}

	trimmed := output[jsonStart : jsonEnd+1]
	parsed := map[string]interface{}{}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, err
	}

	values := map[string]string{}
	for key, raw := range parsed {
		switch v := raw.(type) {
		case string:
			values[key] = v
		case float64:
			values[key] = strconv.FormatInt(int64(v), 10)
		case int:
			values[key] = strconv.Itoa(v)
		}
	}

	return values, nil
}

func parseShowVersionOutput(output string) map[string]string {
	values := map[string]string{}
	reader := strings.NewReader(output)
	scanner := bufio.NewScanner(reader)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "Docker images:") {
			break
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "SONiC Software Version":
			values["sonic_version"] = value
		case "SONiC OS Version":
			values["sonic_os_version"] = value
		case "Distribution":
			values["debian_version"] = strings.TrimPrefix(value, "Debian ")
		case "Kernel":
			values["kernel_version"] = value
		case "Build commit":
			values["build_commit"] = value
		case "Build date":
			values["build_date"] = value
		case "Built by":
			values["built_by"] = value
		case "Platform":
			values["platform"] = value
		case "HwSKU":
			values["hwsku"] = value
		case "ASIC":
			values["asic"] = value
		case "ASIC Count":
			values["asic_count"] = value
		case "Serial Number":
			values["serial"] = value
		case "Model Number":
			values["model"] = value
		case "Hardware Revision":
			values["revision"] = value
		}
	}

	return values
}

func parseSysEEPROMOutput(output string) map[string]string {
	values := map[string]string{}
	reader := strings.NewReader(output)
	scanner := bufio.NewScanner(reader)

	for scanner.Scan() {
		line := scanner.Text()

		if match := serialEEPROMRe.FindStringSubmatch(line); len(match) == 2 {
			values["serial"] = strings.TrimSpace(match[1])
		}
		if match := productEEPROMRe.FindStringSubmatch(line); len(match) == 2 {
			values["model"] = strings.TrimSpace(match[1])
		}
		if match := revisionEEPROMRe.FindStringSubmatch(line); len(match) == 2 {
			values["revision"] = strings.TrimSpace(match[1])
		}
		if values["revision"] == "" {
			if match := deviceVersionEEPROMRe.FindStringSubmatch(line); len(match) == 2 {
				values["revision"] = strings.TrimSpace(match[1])
			}
		}
	}

	return values
}

func readSingleLineFile(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(content)), nil
}

func normalizeSystemValue(value string) string {
	trimmed := strings.TrimSpace(strings.Trim(value, "'\""))
	if trimmed == "" {
		return ""
	}

	lowered := strings.ToLower(trimmed)
	if lowered == "n/a" || lowered == "na" || lowered == "null" {
		return ""
	}

	return trimmed
}
