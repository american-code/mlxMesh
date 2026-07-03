package tests

import (
	"testing"
	"time"

	"github.com/open-inference-mesh/oim/internal/nodeconfig"
)

func mustTime(t *testing.T, layout, value string) time.Time {
	t.Helper()
	tm, err := time.Parse(layout, value)
	if err != nil {
		t.Fatalf("parse time %q: %v", value, err)
	}
	return tm
}

func TestScheduleAlwaysModeIsAlwaysActive(t *testing.T) {
	s := nodeconfig.Schedule{} // zero value
	now := mustTime(t, "2006-01-02T15:04", "2026-07-02T03:00")
	if !s.IsActiveNow(now) {
		t.Error("zero-value Schedule (implicit 'always') should always be active")
	}
}

func TestScheduleSameDayWindow(t *testing.T) {
	s := nodeconfig.Schedule{Mode: nodeconfig.ScheduleModeWindow, DailyStart: "09:00", DailyEnd: "17:00"}

	inside := mustTime(t, "2006-01-02T15:04", "2026-07-02T12:00") // Thursday
	if !s.IsActiveNow(inside) {
		t.Error("12:00 should be active within a 09:00-17:00 window")
	}

	before := mustTime(t, "2006-01-02T15:04", "2026-07-02T08:00")
	if s.IsActiveNow(before) {
		t.Error("08:00 should be inactive before a 09:00-17:00 window")
	}

	after := mustTime(t, "2006-01-02T15:04", "2026-07-02T18:00")
	if s.IsActiveNow(after) {
		t.Error("18:00 should be inactive after a 09:00-17:00 window")
	}

	// End is exclusive.
	atEnd := mustTime(t, "2006-01-02T15:04", "2026-07-02T17:00")
	if s.IsActiveNow(atEnd) {
		t.Error("17:00 (the end boundary) should be exclusive/inactive")
	}
}

func TestScheduleOvernightWindowCrossesMidnight(t *testing.T) {
	s := nodeconfig.Schedule{Mode: nodeconfig.ScheduleModeWindow, DailyStart: "22:00", DailyEnd: "07:00"}

	lateNight := mustTime(t, "2006-01-02T15:04", "2026-07-02T23:30") // Thursday night
	if !s.IsActiveNow(lateNight) {
		t.Error("23:30 should be active within a 22:00-07:00 overnight window")
	}

	earlyMorning := mustTime(t, "2006-01-02T15:04", "2026-07-03T05:00") // Friday early morning
	if !s.IsActiveNow(earlyMorning) {
		t.Error("05:00 should be active within a 22:00-07:00 overnight window (after midnight)")
	}

	midday := mustTime(t, "2006-01-02T15:04", "2026-07-02T12:00")
	if s.IsActiveNow(midday) {
		t.Error("12:00 should be inactive outside a 22:00-07:00 overnight window")
	}
}

func TestScheduleDayRestrictionSameDayWindow(t *testing.T) {
	s := nodeconfig.Schedule{
		Mode: nodeconfig.ScheduleModeWindow, DailyStart: "09:00", DailyEnd: "17:00",
		Days: []string{"sat", "sun"},
	}

	// 2026-07-04 is a Saturday.
	saturday := mustTime(t, "2006-01-02T15:04", "2026-07-04T12:00")
	if !s.IsActiveNow(saturday) {
		t.Error("Saturday should be active when Days includes sat")
	}

	// 2026-07-02 is a Thursday.
	thursday := mustTime(t, "2006-01-02T15:04", "2026-07-02T12:00")
	if s.IsActiveNow(thursday) {
		t.Error("Thursday should be inactive when Days is [sat, sun]")
	}
}

func TestScheduleDayRestrictionOvernightWindowKeyedToStartDay(t *testing.T) {
	// Overnight window that only runs Friday night into Saturday morning.
	s := nodeconfig.Schedule{
		Mode: nodeconfig.ScheduleModeWindow, DailyStart: "22:00", DailyEnd: "07:00",
		Days: []string{"fri"},
	}

	// 2026-07-03 is a Friday.
	fridayNight := mustTime(t, "2006-01-02T15:04", "2026-07-03T23:00")
	if !s.IsActiveNow(fridayNight) {
		t.Error("Friday 23:00 should be active — the window starts on Friday")
	}

	// 2026-07-04 is the Saturday morning continuation of Friday night's window.
	saturdayMorning := mustTime(t, "2006-01-02T15:04", "2026-07-04T05:00")
	if !s.IsActiveNow(saturdayMorning) {
		t.Error("Saturday 05:00 should still be active — it's the tail of Friday night's window")
	}

	// Saturday NIGHT (a new overnight window starting on Saturday) must not
	// match, since Days only allows windows that START on Friday.
	saturdayNight := mustTime(t, "2006-01-02T15:04", "2026-07-04T23:00")
	if s.IsActiveNow(saturdayNight) {
		t.Error("Saturday 23:00 should be inactive — a new window starting Saturday isn't in Days=[fri]")
	}
}

func TestScheduleMalformedTimesFailOpen(t *testing.T) {
	s := nodeconfig.Schedule{Mode: nodeconfig.ScheduleModeWindow, DailyStart: "not-a-time", DailyEnd: "07:00"}
	now := mustTime(t, "2006-01-02T15:04", "2026-07-02T12:00")
	if !s.IsActiveNow(now) {
		t.Error("malformed schedule times should fail open (always active), not silently go dark")
	}
}
