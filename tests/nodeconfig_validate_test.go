package tests

import (
	"strings"
	"testing"

	"github.com/open-inference-mesh/oim/internal/nodeconfig"
)

func TestValidateAcceptsDefault(t *testing.T) {
	if err := nodeconfig.Validate(nodeconfig.Default()); err != nil {
		t.Fatalf("Default() config should be valid, got: %v", err)
	}
}

func TestValidateAcceptsFullValidConfig(t *testing.T) {
	cfg := nodeconfig.Config{
		ExoURL:               "http://localhost:52415",
		MemoryCapPct:         0.4,
		GeographicHint:       "us",
		ReachabilityEndpoint: "http://192.168.1.10:8765",
		PodEndpoint:          "https://pod.example.com:9000",
		SensitivityCap:       "high_requires_attestation",
		Schedule: nodeconfig.Schedule{
			Mode:       nodeconfig.ScheduleModeWindow,
			DailyStart: "22:00",
			DailyEnd:   "07:00",
			Days:       []string{"mon", "fri"},
		},
	}
	if err := nodeconfig.Validate(cfg); err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
}

func TestValidateRejectsBadMemoryCap(t *testing.T) {
	for _, pct := range []float64{0, -0.1, 1.5} {
		cfg := nodeconfig.Default()
		cfg.MemoryCapPct = pct
		if err := nodeconfig.Validate(cfg); err == nil {
			t.Errorf("memory_cap_pct=%g should be rejected", pct)
		}
	}
}

func TestValidateRejectsBadSensitivity(t *testing.T) {
	cfg := nodeconfig.Default()
	cfg.SensitivityCap = "ultra"
	if err := nodeconfig.Validate(cfg); err == nil {
		t.Error("unknown sensitivity_cap should be rejected")
	}
}

func TestValidateRejectsNonHTTPEndpoint(t *testing.T) {
	cfg := nodeconfig.Default()
	cfg.PodEndpoint = "ftp://pod.example.com"
	if err := nodeconfig.Validate(cfg); err == nil {
		t.Error("non-http pod_endpoint should be rejected")
	}
}

func TestValidateRejectsWindowWithBadTimes(t *testing.T) {
	cfg := nodeconfig.Default()
	cfg.Schedule = nodeconfig.Schedule{Mode: nodeconfig.ScheduleModeWindow, DailyStart: "25:00", DailyEnd: "bogus"}
	if err := nodeconfig.Validate(cfg); err == nil {
		t.Error("window mode with invalid HH:MM should be rejected")
	}
}

func TestValidateRejectsBadWeekday(t *testing.T) {
	cfg := nodeconfig.Default()
	cfg.Schedule = nodeconfig.Schedule{Mode: nodeconfig.ScheduleModeWindow, DailyStart: "22:00", DailyEnd: "07:00", Days: []string{"funday"}}
	if err := nodeconfig.Validate(cfg); err == nil {
		t.Error("invalid weekday should be rejected")
	}
}

// TestValidateReportsAllErrorsAtOnce confirms the joined-error behavior: an
// operator fixing a config wants the whole list, not one-at-a-time.
func TestValidateReportsAllErrorsAtOnce(t *testing.T) {
	cfg := nodeconfig.Config{
		MemoryCapPct:   2.0,         // bad
		SensitivityCap: "nope",      // bad
		PodEndpoint:    "not-a-url", // bad
	}
	err := nodeconfig.Validate(cfg)
	if err == nil {
		t.Fatal("expected errors")
	}
	msg := err.Error()
	for _, want := range []string{"memory_cap_pct", "sensitivity_cap", "pod_endpoint"} {
		if !strings.Contains(msg, want) {
			t.Errorf("joined error missing %q; got: %v", want, msg)
		}
	}
}
