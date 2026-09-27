package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestOccurrenceIsDurableBeforeAttemptAndIdentityIsStable(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	due := time.Date(2026, 9, 26, 10, 15, 42, 0, time.UTC)
	input := DueInput{Schedule: "every-minute", Task: "publish", TaskGenerationID: "revision-a-task", ApplicationRevision: "revision-a", ConfigurationDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", FencingToken: 7, DueAt: due, TaskUnit: "provision-lab-publish@.service", MaxAttempts: 1, RetryDelay: 10 * time.Second, Overlap: "forbid"}

	occurrence, invocation, inserted, err := store.RecordDue(context.Background(), input, due.Add(time.Second))
	if err != nil || !inserted {
		t.Fatalf("record due: inserted=%v err=%v", inserted, err)
	}
	if occurrence.Disposition != "recorded" || invocation.Outcome != "pending" || len(invocation.Attempts) != 0 {
		t.Fatalf("work was not durably recorded before delivery: %#v %#v", occurrence, invocation)
	}
	repeatedOccurrence, repeatedInvocation, inserted, err := store.RecordDue(context.Background(), input, due.Add(2*time.Second))
	if err != nil || inserted || repeatedOccurrence.ID != occurrence.ID || repeatedInvocation.ID != invocation.ID {
		t.Fatalf("same due occurrence did not reuse identity: inserted=%v err=%v", inserted, err)
	}

	attempt, err := store.BeginAttempt(context.Background(), invocation.ID, "provision-lab-publish@"+invocation.ID+".service", due.Add(3*time.Second))
	if err != nil || attempt != 1 {
		t.Fatalf("begin attempt: number=%d err=%v", attempt, err)
	}
	if err := store.CompleteAttempt(context.Background(), invocation.ID, attempt, "succeeded", "/evidence/task.jsonl", due.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	latestOccurrence, latestInvocation, err := store.Latest(context.Background(), "every-minute")
	if err != nil {
		t.Fatal(err)
	}
	if latestOccurrence.Disposition != "succeeded" || latestInvocation.Outcome != "succeeded" || len(latestInvocation.Attempts) != 1 || latestInvocation.Attempts[0].Outcome != "succeeded" {
		t.Fatalf("completed attempt missing from ledger: %#v %#v", latestOccurrence, latestInvocation)
	}
}

func TestConcurrentAttemptClaimsEnforceStoredOverlapPolicy(t *testing.T) {
	for _, overlap := range []string{"forbid", "allow"} {
		t.Run(overlap, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ledger.db")
			first, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close()
			second, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			now := time.Date(2026, 9, 27, 10, 15, 0, 0, time.UTC)
			input := DueInput{Schedule: "every-minute", Task: "publish", TaskGenerationID: "revision-a-task", TaskUnit: "provision-lab-publish@.service", ApplicationRevision: "revision-a", ConfigurationDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", FencingToken: 7, MaxAttempts: 2, RetryDelay: 10 * time.Second, Overlap: overlap}
			input.DueAt = now
			_, one, _, err := first.RecordDue(context.Background(), input, now)
			if err != nil {
				t.Fatal(err)
			}
			input.DueAt = now.Add(time.Minute)
			_, two, _, err := first.RecordDue(context.Background(), input, now)
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			results := make(chan error, 2)
			for index, item := range []struct {
				store *Store
				id    string
			}{{first, one.ID}, {second, two.ID}} {
				wg.Add(1)
				go func(index int, item struct {
					store *Store
					id    string
				}) {
					defer wg.Done()
					_, err := item.store.BeginAttempt(context.Background(), item.id, "provision-lab-publish@"+item.id+".service", now.Add(time.Duration(index)*time.Second))
					results <- err
				}(index, item)
			}
			wg.Wait()
			close(results)
			success, blocked := 0, 0
			for err := range results {
				if err == nil {
					success++
				} else if errors.Is(err, ErrOverlap) {
					blocked++
				} else {
					t.Fatalf("unexpected claim result: %v", err)
				}
			}
			if overlap == "forbid" && (success != 1 || blocked != 1) {
				t.Fatalf("forbidden concurrent claims: success=%d blocked=%d", success, blocked)
			}
			if overlap == "allow" && (success != 2 || blocked != 0) {
				t.Fatalf("allowed concurrent claims: success=%d blocked=%d", success, blocked)
			}
		})
	}
}

func TestReadOnlyOpenDoesNotCreateALedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if _, err := OpenReadOnly(path); err == nil {
		t.Fatal("missing ledger opened read-only")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("read-only inspection created ledger: %v", err)
	}
}

func TestSkippedDSTGapStoresSeparateWallClockCursor(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	wall := time.Date(2026, 3, 29, 2, 30, 0, 0, time.UTC)
	input := DueInput{
		Schedule: "daily", Task: "publish", TaskGenerationID: "revision-a-task",
		ApplicationRevision: "revision-a", ConfigurationDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		FencingToken: 7, DueAt: wall, WallAt: wall, TaskUnit: "provision-lab-publish@.service",
		MaxAttempts: 1, RetryDelay: 10 * time.Second, Overlap: "forbid",
	}
	occurrence, _, inserted, err := store.RecordSkipped(context.Background(), input, wall.Add(time.Hour), "skipped-dst-gap")
	if err != nil || !inserted {
		t.Fatalf("record skipped gap: inserted=%v err=%v", inserted, err)
	}
	if occurrence.WallDueAt != "2026-03-29T02:30" {
		t.Fatalf("wall-clock label not reported: %+v", occurrence)
	}
	latest, err := store.LatestWallAt(context.Background(), "daily")
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || !latest.Equal(wall) {
		t.Fatalf("wall-clock cursor was not retained separately from UTC instant: %v", latest)
	}
}
