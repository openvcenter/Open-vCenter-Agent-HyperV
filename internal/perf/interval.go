package perf

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// DefaultInterval is the collection window used until the backend pushes
	// its Application Preferences value (set_metrics_config).
	DefaultInterval = 300
	minInterval     = 10
	maxInterval     = 3600

	intervalFileName = "metrics_interval"
)

// ValidInterval clamps a requested interval to what the sampler accepts;
// anything non-positive means the default.
func ValidInterval(seconds int) int {
	switch {
	case seconds <= 0:
		return DefaultInterval
	case seconds < minInterval:
		return minInterval
	case seconds > maxInterval:
		return maxInterval
	}
	return seconds
}

// LoadInterval reads the interval last applied by the backend from dir. The
// file lets an agent restart keep the configured value and lets agent_status,
// which runs in a worker process, report what the service is using.
func LoadInterval(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, intervalFileName))
	if err != nil {
		return DefaultInterval
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return DefaultInterval
	}
	return ValidInterval(v)
}

// SaveInterval stores the applied interval in dir.
func SaveInterval(dir string, seconds int) error {
	return os.WriteFile(filepath.Join(dir, intervalFileName), []byte(strconv.Itoa(ValidInterval(seconds))), 0o644)
}
