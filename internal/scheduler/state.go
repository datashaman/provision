package scheduler

import "provision/internal/host"

const (
	OverlapAllow  = "allow"
	OverlapForbid = "forbid"
)

const (
	OccurrenceDispositionRecorded       = "recorded"
	OccurrenceDispositionRunning        = "running"
	OccurrenceDispositionSucceeded      = "succeeded"
	OccurrenceDispositionFailed         = "failed"
	OccurrenceDispositionTimedOut       = "timed-out"
	OccurrenceDispositionUncertain      = "uncertain"
	OccurrenceDispositionSkippedOverlap = "skipped-overlap"
	OccurrenceDispositionSkippedMissed  = "skipped-missed"
	OccurrenceDispositionSkippedDSTGap  = "skipped-dst-gap"
)

const (
	InvocationOutcomePending        = "pending"
	InvocationOutcomeRunning        = "running"
	InvocationOutcomeSucceeded      = "succeeded"
	InvocationOutcomeFailed         = "failed"
	InvocationOutcomeTimedOut       = "timed-out"
	InvocationOutcomeUncertain      = "uncertain"
	InvocationOutcomeSkippedOverlap = "skipped-overlap"
)

const (
	AttemptOutcomeRunning   = "running"
	AttemptOutcomeSucceeded = "succeeded"
	AttemptOutcomeFailed    = "failed"
	AttemptOutcomeTimedOut  = "timed-out"
	AttemptOutcomeUncertain = "uncertain"
)

func SkippedOccurrenceDisposition(disposition string) bool {
	return disposition == OccurrenceDispositionSkippedMissed || disposition == OccurrenceDispositionSkippedDSTGap
}

func FinishedInvocationOutcome(outcome string) bool {
	return outcome == InvocationOutcomeSucceeded || outcome == InvocationOutcomeSkippedOverlap
}

func RetryableInvocationOutcome(outcome string) bool {
	return outcome == InvocationOutcomePending ||
		outcome == InvocationOutcomeRunning ||
		outcome == InvocationOutcomeFailed ||
		outcome == InvocationOutcomeTimedOut ||
		outcome == InvocationOutcomeUncertain
}

func SupportedAttemptOutcome(outcome string) bool {
	return outcome == AttemptOutcomeSucceeded ||
		outcome == AttemptOutcomeFailed ||
		outcome == AttemptOutcomeTimedOut ||
		outcome == AttemptOutcomeUncertain
}

func InvocationSucceeded(invocation host.TaskInvocationStatus) bool {
	if invocation.Outcome != InvocationOutcomeSucceeded || len(invocation.Attempts) == 0 {
		return false
	}
	return invocation.Attempts[len(invocation.Attempts)-1].Outcome == AttemptOutcomeSucceeded
}
