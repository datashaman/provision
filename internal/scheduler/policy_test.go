package scheduler

import (
	"testing"
	"time"

	"provision/internal/config"
)

func TestEvaluateUsesNamedTimezoneWithoutSilentlyShiftingDSTGap(t *testing.T) {
	now := time.Date(2026, 3, 29, 3, 31, 0, 0, mustLocation(t, "Europe/Berlin"))
	last := time.Date(2026, 3, 28, 2, 30, 0, 0, mustLocation(t, "Europe/Berlin"))
	decisions, err := Evaluate(EvaluationInput{
		Expression:     "30 2 * * *",
		Timezone:       "Europe/Berlin",
		DaylightSaving: "wall-clock",
		MissedRun:      config.ScheduleMissedRun{Mode: "skip"},
		LastWallAt:     &last,
		Now:            now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || decisions[0].Disposition != "skipped-dst-gap" {
		t.Fatalf("DST gap was shifted into a runnable occurrence: %+v", decisions)
	}
	if got := decisions[0].DueAt.Format("15:04"); got != "02:30" {
		t.Fatalf("gap decision did not preserve the explicit local label in evidence: %s", got)
	}
}

func TestEvaluateRecordsDSTFoldOnceForOneWallClockLabel(t *testing.T) {
	location := mustLocation(t, "America/New_York")
	last := time.Date(2026, 10, 31, 1, 30, 0, 0, location)
	now := time.Date(2026, 11, 1, 2, 15, 0, 0, location)
	decisions, err := Evaluate(EvaluationInput{
		Expression:     "30 1 * * *",
		Timezone:       "America/New_York",
		DaylightSaving: "wall-clock",
		MissedRun:      config.ScheduleMissedRun{Mode: "bounded-catch-up", MaxOccurrences: 5},
		LastWallAt:     &last,
		Now:            now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || decisions[0].Disposition != "recorded" {
		t.Fatalf("DST fold produced duplicate or missing wall-clock occurrence: %+v", decisions)
	}
	if got := decisions[0].DueAt.In(location).Format("2006-01-02 15:04"); got != "2026-11-01 01:30" {
		t.Fatalf("fold occurrence changed wall-clock label: %s", got)
	}
}

func TestEvaluateSkipMissedRunRecordsDispositionWithoutRunnableTask(t *testing.T) {
	last := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 27, 10, 3, 0, 0, time.UTC)
	decisions, err := Evaluate(EvaluationInput{
		Expression:     "* * * * *",
		Timezone:       "UTC",
		DaylightSaving: "wall-clock",
		MissedRun:      config.ScheduleMissedRun{Mode: "skip"},
		LastWallAt:     &last,
		Now:            now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := dispositions(decisions), []string{"skipped-missed", "skipped-missed", "recorded"}; !sameStrings(got, want) {
		t.Fatalf("skip missed policy = %v, want %v", got, want)
	}
}

func TestEvaluateSkipMissedRunSkipsASingleOverdueOccurrence(t *testing.T) {
	last := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 27, 10, 30, 0, 0, time.UTC)
	decisions, err := Evaluate(EvaluationInput{
		Expression:     "0 * * * *",
		Timezone:       "UTC",
		DaylightSaving: "wall-clock",
		MissedRun:      config.ScheduleMissedRun{Mode: "skip"},
		LastWallAt:     &last,
		Now:            now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := dispositions(decisions), []string{"skipped-missed"}; !sameStrings(got, want) {
		t.Fatalf("single missed skip policy = %v, want %v", got, want)
	}
}

func TestEvaluateBoundedCatchUpCapsRunnableMissedOccurrences(t *testing.T) {
	last := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 27, 10, 5, 0, 0, time.UTC)
	decisions, err := Evaluate(EvaluationInput{
		Expression:     "* * * * *",
		Timezone:       "UTC",
		DaylightSaving: "wall-clock",
		MissedRun:      config.ScheduleMissedRun{Mode: "bounded-catch-up", MaxOccurrences: 2},
		LastWallAt:     &last,
		Now:            now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := dispositions(decisions), []string{"skipped-missed", "skipped-missed", "skipped-missed", "recorded", "recorded"}; !sameStrings(got, want) {
		t.Fatalf("bounded catch-up = %v, want %v", got, want)
	}
}

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func dispositions(decisions []DueDecision) []string {
	result := make([]string, 0, len(decisions))
	for _, decision := range decisions {
		result = append(result, decision.Disposition)
	}
	return result
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
