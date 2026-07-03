package nodeconfig

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Valid sensitivity caps — kept as local constants (string-equal to
// protocol.SensitivityTier values) so nodeconfig stays dependency-light and
// doesn't import the protocol package just for validation.
var validSensitivityCaps = map[string]bool{
	"low":                       true,
	"moderate":                  true,
	"high_requires_attestation": true,
}

var validWeekdays = map[string]bool{
	"mon": true, "tue": true, "wed": true, "thu": true,
	"fri": true, "sat": true, "sun": true,
}

// Validate reports every problem with a config at once (joined), rather than
// failing on the first — an operator fixing settings wants the whole list, not
// a fix-one-rerun-find-the-next loop. A zero/empty field that has a sensible
// default is NOT an error; only actively-wrong values are.
func Validate(cfg Config) error {
	var errs []error

	if cfg.MemoryCapPct <= 0 || cfg.MemoryCapPct > 1 {
		errs = append(errs, fmt.Errorf("memory_cap_pct must be in (0, 1]; got %g (it is a fraction, e.g. 0.5 = 50%%)", cfg.MemoryCapPct))
	}

	if cfg.SensitivityCap != "" && !validSensitivityCaps[cfg.SensitivityCap] {
		errs = append(errs, fmt.Errorf("sensitivity_cap %q is not one of: low, moderate, high_requires_attestation", cfg.SensitivityCap))
	}

	for _, ep := range []struct {
		name, val string
	}{
		{"exo_url", cfg.ExoURL},
		{"pod_endpoint", cfg.PodEndpoint},
		{"reachability_endpoint", cfg.ReachabilityEndpoint},
	} {
		if ep.val == "" {
			continue // empty is allowed — defaults/auto-derivation apply
		}
		if err := validateHTTPURL(ep.val); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ep.name, err))
		}
	}

	errs = append(errs, validateSchedule(cfg.Schedule)...)

	return errors.Join(errs...)
}

func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%q is not a valid URL: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%q must use http:// or https:// (got scheme %q)", raw, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%q has no host", raw)
	}
	return nil
}

func validateSchedule(s Schedule) []error {
	var errs []error
	switch s.Mode {
	case "", ScheduleModeAlways:
		// No window fields required.
	case ScheduleModeWindow:
		if _, ok := parseHHMM(s.DailyStart); !ok {
			errs = append(errs, fmt.Errorf("schedule.daily_start %q is not valid HH:MM 24-hour time", s.DailyStart))
		}
		if _, ok := parseHHMM(s.DailyEnd); !ok {
			errs = append(errs, fmt.Errorf("schedule.daily_end %q is not valid HH:MM 24-hour time", s.DailyEnd))
		}
	default:
		errs = append(errs, fmt.Errorf("schedule.mode %q is not one of: always, window (or empty)", s.Mode))
	}
	for _, d := range s.Days {
		key := strings.ToLower(strings.TrimSpace(d))
		if !validWeekdays[key] {
			errs = append(errs, fmt.Errorf("schedule.days entry %q is not a valid weekday (mon, tue, wed, thu, fri, sat, sun)", d))
		}
	}
	return errs
}
