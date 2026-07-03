package nodeconfig

import (
	"strconv"
	"strings"
	"time"
)

// ScheduleModeAlways contributes whenever the agent process is running — the
// default (zero value), matching pre-scheduler behavior exactly.
const ScheduleModeAlways = "always"

// ScheduleModeWindow restricts contribution to a daily time window, optionally
// limited to specific weekdays.
const ScheduleModeWindow = "window"

// Schedule controls when this node is allowed to serve mesh jobs — e.g. "only
// overnight, not during my working hours." Unlike the rest of Config, the
// running agent re-reads Schedule on every heartbeat tick rather than only at
// startup, so edits made in the dashboard (or via CLI flags, which write
// through to this same file) take effect live without a restart — a schedule
// only useful "after you restart the process" defeats the point.
type Schedule struct {
	Mode string `json:"mode"` // ScheduleModeAlways | ScheduleModeWindow; "" behaves as Always
	// DailyStart/DailyEnd are "HH:MM" 24-hour local time, e.g. "22:00".
	// End < Start means the window crosses midnight (e.g. 22:00-07:00 =
	// overnight only). Ignored when Mode != ScheduleModeWindow.
	DailyStart string `json:"daily_start"`
	DailyEnd   string `json:"daily_end"`
	// Days restricts the window to specific weekdays: lowercase three-letter
	// prefixes ("mon", "tue", ... "sun"). Empty means every day. For an
	// overnight window, a day names the night the window STARTS on (e.g.
	// Days=["fri"] with 22:00-07:00 covers Friday 22:00 through Saturday 07:00).
	Days []string `json:"days"`
}

// IsActiveNow reports whether the mesh is allowed to use this node at local
// time t. Any unrecognized/malformed configuration fails OPEN (returns true)
// rather than silently going dark — a typo in a time string should not make a
// contributor's node quietly stop earning credits with no obvious cause.
func (s Schedule) IsActiveNow(t time.Time) bool {
	if s.Mode != ScheduleModeWindow {
		return true
	}
	start, startOK := parseHHMM(s.DailyStart)
	end, endOK := parseHHMM(s.DailyEnd)
	if !startOK || !endOK {
		return true
	}
	nowMin := t.Hour()*60 + t.Minute()

	if start <= end {
		// Same-day window (e.g. 09:00-17:00).
		return dayAllowed(s.Days, t.Weekday()) && nowMin >= start && nowMin < end
	}
	// Overnight window (e.g. 22:00-07:00), keyed to the day it starts on.
	if nowMin >= start {
		return dayAllowed(s.Days, t.Weekday())
	}
	if nowMin < end {
		return dayAllowed(s.Days, t.Weekday()-1)
	}
	return false
}

func dayAllowed(days []string, wd time.Weekday) bool {
	if len(days) == 0 {
		return true
	}
	normalized := time.Weekday((int(wd)%7 + 7) % 7)
	name := strings.ToLower(normalized.String()[:3])
	for _, d := range days {
		if strings.ToLower(strings.TrimSpace(d)) == name {
			return true
		}
	}
	return false
}

// parseHHMM parses "HH:MM" (24-hour) into minutes-since-midnight.
func parseHHMM(s string) (minutes int, ok bool) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, false
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}
