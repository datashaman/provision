package scheduler

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"provision/internal/config"
)

type EvaluationInput struct {
	Expression     string
	Timezone       string
	DaylightSaving string
	MissedRun      config.ScheduleMissedRun
	LastWallAt     *time.Time
	Now            time.Time
}

type DueDecision struct {
	DueAt       time.Time
	WallAt      time.Time
	Disposition string
}

func Evaluate(input EvaluationInput) ([]DueDecision, error) {
	if input.DaylightSaving != "wall-clock" {
		return nil, errors.New("only wall-clock daylight-saving policy is supported")
	}
	schedule, err := parseCron(input.Expression)
	if err != nil {
		return nil, err
	}
	location, err := time.LoadLocation(input.Timezone)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q", input.Timezone)
	}
	now := wallMinute(input.Now.In(location))
	var start time.Time
	if input.LastWallAt == nil {
		start = now
	} else {
		start = addWallMinutes(wallMinute(*input.LastWallAt), 1)
	}
	if start.After(now) {
		return nil, nil
	}
	var decisions []DueDecision
	for cursor := start; !cursor.After(now); cursor = addWallMinutes(cursor, 1) {
		if !schedule.matches(cursor) {
			continue
		}
		due, ok := resolveWallClock(cursor, location)
		if !ok {
			decisions = append(decisions, DueDecision{DueAt: cursor, WallAt: cursor, Disposition: "skipped-dst-gap"})
			continue
		}
		decisions = append(decisions, DueDecision{DueAt: due, WallAt: cursor, Disposition: "recorded"})
	}
	return applyMissedPolicy(decisions, input.MissedRun, now)
}

func applyMissedPolicy(decisions []DueDecision, policy config.ScheduleMissedRun, now time.Time) ([]DueDecision, error) {
	catchable := 0
	switch policy.Mode {
	case "skip":
		catchable = 0
	case "bounded-catch-up":
		if policy.MaxOccurrences < 1 || policy.MaxOccurrences > 100 {
			return nil, errors.New("bounded catch-up requires maxOccurrences from 1 through 100")
		}
		catchable = policy.MaxOccurrences
	default:
		return nil, errors.New("unsupported missed-run policy")
	}
	recordedIndexes := make([]int, 0, len(decisions))
	for index, decision := range decisions {
		if decision.Disposition == "recorded" {
			recordedIndexes = append(recordedIndexes, index)
		}
	}
	if len(recordedIndexes) == 0 {
		return decisions, nil
	}
	limit := 1
	if policy.Mode == "bounded-catch-up" {
		limit = catchable
	}
	firstCatchable := len(recordedIndexes) - limit
	if firstCatchable < 0 {
		firstCatchable = 0
	}
	for ordinal, decisionIndex := range recordedIndexes {
		if decisions[decisionIndex].WallAt.Before(now) && (policy.Mode == "skip" || ordinal < firstCatchable) {
			decisions[decisionIndex].Disposition = "skipped-missed"
		}
	}
	return decisions, nil
}

func resolveWallClock(value time.Time, location *time.Location) (time.Time, bool) {
	due := time.Date(value.Year(), value.Month(), value.Day(), value.Hour(), value.Minute(), 0, 0, location)
	roundtrip := due.In(location)
	if roundtrip.Year() != value.Year() || roundtrip.Month() != value.Month() || roundtrip.Day() != value.Day() || roundtrip.Hour() != value.Hour() || roundtrip.Minute() != value.Minute() {
		return time.Time{}, false
	}
	return due.UTC(), true
}

func wallMinute(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), value.Hour(), value.Minute(), 0, 0, time.UTC)
}

func addWallMinutes(value time.Time, minutes int) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), value.Hour(), value.Minute()+minutes, 0, 0, time.UTC)
}

type cronSchedule struct {
	minute     cronField
	hour       cronField
	dayOfMonth cronField
	month      cronField
	weekday    cronField
}

type cronField struct {
	any    bool
	values map[int]bool
}

func parseCron(expression string) (cronSchedule, error) {
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return cronSchedule{}, errors.New("Schedule expression must have five fields")
	}
	minute, err := parseCronField(fields[0], 0, 59)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("minute field: %w", err)
	}
	hour, err := parseCronField(fields[1], 0, 23)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("hour field: %w", err)
	}
	dayOfMonth, err := parseCronField(fields[2], 1, 31)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("day-of-month field: %w", err)
	}
	month, err := parseCronField(fields[3], 1, 12)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("month field: %w", err)
	}
	weekday, err := parseCronField(fields[4], 0, 7)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("weekday field: %w", err)
	}
	return cronSchedule{minute: minute, hour: hour, dayOfMonth: dayOfMonth, month: month, weekday: weekday}, nil
}

func parseCronField(field string, min, max int) (cronField, error) {
	if field == "*" {
		return cronField{any: true}, nil
	}
	result := cronField{values: map[int]bool{}}
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return cronField{}, errors.New("empty list item")
		}
		step := 1
		base := part
		if before, after, ok := strings.Cut(part, "/"); ok {
			base = before
			parsed, err := strconv.Atoi(after)
			if err != nil || parsed < 1 {
				return cronField{}, errors.New("invalid step")
			}
			step = parsed
		}
		start, end := min, max
		switch {
		case base == "*":
		case strings.Contains(base, "-"):
			left, right, _ := strings.Cut(base, "-")
			parsedStart, err := strconv.Atoi(left)
			if err != nil {
				return cronField{}, errors.New("invalid range start")
			}
			parsedEnd, err := strconv.Atoi(right)
			if err != nil {
				return cronField{}, errors.New("invalid range end")
			}
			start, end = parsedStart, parsedEnd
		default:
			parsed, err := strconv.Atoi(base)
			if err != nil {
				return cronField{}, errors.New("invalid value")
			}
			start, end = parsed, parsed
		}
		if start < min || end > max || start > end {
			return cronField{}, errors.New("value out of range")
		}
		for value := start; value <= end; value += step {
			if max == 7 && value == 7 {
				result.values[0] = true
			} else {
				result.values[value] = true
			}
		}
	}
	return result, nil
}

func (s cronSchedule) matches(value time.Time) bool {
	weekday := int(value.Weekday())
	return s.minute.matches(value.Minute()) &&
		s.hour.matches(value.Hour()) &&
		s.dayOfMonth.matches(value.Day()) &&
		s.month.matches(int(value.Month())) &&
		s.weekday.matches(weekday)
}

func (f cronField) matches(value int) bool {
	return f.any || f.values[value]
}
