package collector

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func parseBoolish(value string) (float64, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "up", "yes", "1", "moduleready", "datapathactivated", "ok":
		return 1, true
	case "false", "down", "no", "0", "moduleabsent", "modulefault":
		return 0, true
	default:
		return 0, false
	}
}

func parseCounterLike(value string) (float64, bool) {
	parsedValue, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0, false
	}

	return parsedValue, true
}

func parseEventTime(value string) (float64, bool) {
	trimmedValue := strings.TrimSpace(value)
	if trimmedValue == "" || strings.EqualFold(trimmedValue, "never") {
		return 0, false
	}

	parsedTime, err := time.Parse("Mon Jan 02 15:04:05 2006", trimmedValue)
	if err != nil {
		return 0, false
	}

	return float64(parsedTime.Unix()), true
}

func normalizeProcessName(command string) string {
	trimmedCommand := strings.TrimSpace(command)
	if trimmedCommand == "" {
		return "unknown"
	}

	firstToken := strings.Fields(trimmedCommand)[0]
	if firstToken == "" {
		return "unknown"
	}

	return filepath.Base(firstToken)
}

func familyFromPrefix(value string) string {
	if strings.Contains(value, ":") {
		return "ipv6"
	}

	return "ipv4"
}

func parseKeySuffix(key, prefix string) (string, error) {
	if !strings.HasPrefix(key, prefix) {
		return "", fmt.Errorf("key %q does not start with %q", key, prefix)
	}

	suffix := strings.TrimPrefix(key, prefix)
	if suffix == "" {
		return "", fmt.Errorf("key %q has empty suffix", key)
	}

	return suffix, nil
}
