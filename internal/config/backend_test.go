package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackendConfigOverlay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	ini := "[agent]\nhost_id = h1\nrabbitmq_url = amqp://x\nmetrics_interval = 300\nrefresh_interval_vms = 180\n"
	if err := os.WriteFile(path, []byte(ini), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForMessaging(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MetricsInterval != 300 {
		t.Errorf("without backend file: metrics_interval = %d", cfg.MetricsInterval)
	}

	if err := SaveBackendValue(dir, "metrics_interval", "60"); err != nil {
		t.Fatal(err)
	}
	if err := SaveBackendValue(dir, "refresh_interval_vms", "90"); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadForMessaging(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MetricsInterval != 60 || cfg.RefreshIntervalVMs != 90 {
		t.Errorf("backend values not applied: metrics=%d vms=%d", cfg.MetricsInterval, cfg.RefreshIntervalVMs)
	}
	if cfg.HostID != "h1" {
		t.Errorf("config.ini value lost: host_id = %q", cfg.HostID)
	}

	// A broken backend file must not keep the agent from starting.
	if err := os.WriteFile(filepath.Join(dir, BackendConfigFileName), []byte("[agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if cfg, err = LoadForMessaging(path); err != nil || cfg.MetricsInterval != 300 {
		t.Errorf("broken backend file: cfg=%+v err=%v", cfg, err)
	}
}
