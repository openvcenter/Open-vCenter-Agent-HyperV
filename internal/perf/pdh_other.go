//go:build !windows

package perf

import "errors"

// NewSource is only available on Windows; the pure aggregation logic in this
// package still builds and tests elsewhere.
func NewSource() (Source, error) {
	return nil, errors.New("performance counters are only available on Windows")
}
