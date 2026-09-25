package retention

import (
	"errors"
	"time"
)

const (
	MinimumWindow = time.Minute
	MaximumWindow = 30 * 24 * time.Hour
)

type Window string

type Policy string

const PolicyRollbackWindow Policy = "rollback-window"

func ParseWindow(value string) (Window, error) {
	duration, err := time.ParseDuration(value)
	if err != nil || duration.String() != value {
		return "", errors.New("rollback window must be a canonical duration")
	}
	if duration < MinimumWindow || duration > MaximumWindow {
		return "", errors.New("rollback window is outside the supported range")
	}
	return Window(value), nil
}

func (window Window) Duration() (time.Duration, error) {
	parsed, err := ParseWindow(string(window))
	if err != nil {
		return 0, err
	}
	return time.ParseDuration(string(parsed))
}
