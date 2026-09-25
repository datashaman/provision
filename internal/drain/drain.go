package drain

import (
	"errors"
	"time"
)

type Mode string

const ModeBoundedHTTP Mode = "bounded-http"

type Bound string

func ParseBound(value string) (Bound, error) {
	duration, err := time.ParseDuration(value)
	if err != nil || duration.String() != value {
		return "", errors.New("drain bound is not a canonical duration")
	}
	if duration < time.Second || duration > 5*time.Minute {
		return "", errors.New("drain bound is outside the supported range")
	}
	return Bound(value), nil
}

func (bound Bound) Duration() (time.Duration, error) {
	parsed, err := ParseBound(string(bound))
	if err != nil {
		return 0, err
	}
	return time.ParseDuration(string(parsed))
}
