package scheduler

import (
	"context"
	"os"
	"path/filepath"
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
	input := DueInput{Schedule: "every-minute", Task: "publish", TaskGenerationID: "revision-a-task", ApplicationRevision: "revision-a", ConfigurationDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", FencingToken: 7, DueAt: due}

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

func TestReadOnlyOpenDoesNotCreateALedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if _, err := OpenReadOnly(path); err == nil {
		t.Fatal("missing ledger opened read-only")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("read-only inspection created ledger: %v", err)
	}
}
