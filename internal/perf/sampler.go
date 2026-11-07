package perf

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

const (
	// SampleInterval is how often the counters are read; each published window
	// carries the mean and peak of these samples.
	SampleInterval = 5 * time.Second
	// topologyRefresh is the regular VM topology refresh. A counter instance
	// that appears unmapped forces an earlier one, but not more often than
	// topologyMinAge; instances still unmapped right after a refresh (an ISO,
	// a host vNIC) are ignored until the next one.
	topologyRefresh = 30 * time.Minute
	topologyMinAge  = 1 * time.Minute
	topologyTimeout = 60 * time.Second
)

// PublishFunc sends one payload to the given state queue kind
// ("host_metrics" or "vm_metrics").
type PublishFunc func(ctx context.Context, kind string, payload any) error

// Sampler reads the counters every SampleInterval and publishes one aggregate
// per collection interval. It runs in the agent service process.
type Sampler struct {
	src      Source
	publish  PublishFunc
	fetch    func(context.Context) (*Topology, error)
	logger   *slog.Logger
	interval atomic.Int64 // seconds
	changed  chan struct{}
	now      func() time.Time
}

func NewSampler(src Source, interval int, publish PublishFunc, logger *slog.Logger) *Sampler {
	s := &Sampler{
		src:     src,
		publish: publish,
		fetch:   FetchTopology,
		logger:  logger,
		changed: make(chan struct{}, 1),
		now:     time.Now,
	}
	s.interval.Store(int64(ValidInterval(interval)))
	return s
}

// Interval returns the collection interval in seconds.
func (s *Sampler) Interval() int { return int(s.interval.Load()) }

// SetInterval changes the collection interval; the current window is published
// on the next sample if it is already older than the new interval.
func (s *Sampler) SetInterval(seconds int) {
	s.interval.Store(int64(ValidInterval(seconds)))
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// Run samples until ctx is done.
func (s *Sampler) Run(ctx context.Context) {
	defer s.src.Close()

	var (
		topo       *Topology
		topoAt     time.Time
		topoStale  bool
		topoFresh  bool            // the next sample is the first after a refresh
		ignored    map[string]bool // instances that stayed unmapped after it
		win        = newWindow(s.now())
		ticker     = time.NewTicker(SampleInterval)
		refreshTop = func() {
			age := s.now().Sub(topoAt)
			if !topoAt.IsZero() && age < topologyRefresh && (!topoStale || age < topologyMinAge) {
				return
			}
			tctx, cancel := context.WithTimeout(ctx, topologyTimeout)
			defer cancel()
			t, err := s.fetch(tctx)
			topoAt = s.now()
			if err != nil {
				s.logger.Warn("metrics: VM topology refresh failed", "error", err)
				return
			}
			topo, topoStale, topoFresh = t, false, true
		}
	)
	defer ticker.Stop()

	s.logger.Info("metrics sampler started",
		"sample_interval", SampleInterval.String(), "interval_seconds", s.Interval())

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.changed:
			s.logger.Info("metrics interval changed", "interval_seconds", s.Interval())
			continue
		case <-ticker.C:
		}

		refreshTop()
		smp, err := s.src.Sample()
		if err != nil {
			s.logger.Warn("metrics: sample failed", "error", err)
			continue
		}
		unmatched := win.add(smp, topo)
		if topoFresh {
			ignored, topoFresh = unmatched, false
		} else if hasNew(unmatched, ignored) {
			topoStale = true
		}

		end := s.now()
		if end.Sub(win.start) < time.Duration(s.Interval())*time.Second {
			continue
		}
		s.flush(ctx, win, topo, end)
		win = newWindow(end)
	}
}

// flush publishes the window. Metrics are best-effort: a failed publish drops
// the window instead of queueing it.
func (s *Sampler) flush(ctx context.Context, win *window, topo *Topology, end time.Time) {
	if win.samples == 0 {
		return
	}
	reportedAt := end.UTC().Format(time.RFC3339Nano)
	if err := s.publish(ctx, "host_metrics", win.hostPayload(reportedAt)); err != nil {
		s.logger.Warn("metrics: host_metrics not published", "error", err)
	}
	if err := s.publish(ctx, "vm_metrics", win.vmPayload(reportedAt, topo)); err != nil {
		s.logger.Warn("metrics: vm_metrics not published", "error", err)
	}
	s.logger.Debug("metrics published", "samples", win.samples, "vms", len(win.vms))
}

// hasNew reports whether unmatched holds an instance not in ignored.
func hasNew(unmatched, ignored map[string]bool) bool {
	for inst := range unmatched {
		if !ignored[inst] {
			return true
		}
	}
	return false
}
