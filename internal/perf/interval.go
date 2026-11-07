package perf

const (
	// DefaultInterval is the collection window used until the backend pushes
	// its Application Preferences value (set_metrics_config).
	DefaultInterval = 300
	minInterval     = 10
	maxInterval     = 3600
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
