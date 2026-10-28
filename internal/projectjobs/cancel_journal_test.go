package projectjobs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCancelPendingJournalPreservesHistoryAndNeverStartsWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	j := journal{Version: journalVersion, NextSequence: 1}
	for _, status := range []Status{Queued, Running, Canceling, Succeeded, Failed, Canceled, Aborted} {
		j.Jobs = append(j.Jobs, Job{ID: string(status), Request: Request{ProjectID: "book", ProviderID: "model", Kind: "batch"}, Status: status, CreatedAt: time.Now().UTC()})
	}
	if err := saveJournal(path, j); err != nil {
		t.Fatal(err)
	}
	if err := CancelPendingJournal(path); err != nil {
		t.Fatal(err)
	}
	saved, err := loadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, before := range j.Jobs {
		after := saved.Jobs[i]
		if before.Status.Terminal() {
			if after.Status != before.Status || !after.FinishedAt.Equal(before.FinishedAt) {
				t.Errorf("completed history changed: %+v", after)
			}
		} else if after.Status != Canceled || after.FinishedAt.IsZero() || after.Error != ErrCanceled.Error() {
			t.Errorf("unfinished work not durably canceled: %+v", after)
		}
	}
	if len(saved.Events) != 3 || saved.NextSequence != 4 {
		t.Fatalf("cancellation audit events missing: %+v", saved.Events)
	}
	if err := CancelPendingJournal(path); err != nil {
		t.Fatal(err)
	}
	again, err := loadJournal(path)
	if err != nil || len(again.Events) != 3 {
		t.Fatalf("repeated cancellation was not idempotent: %+v %v", again, err)
	}
}

func TestCancelPendingJournalDoesNotCreateOrOverwriteInvalidData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	if err := CancelPendingJournal(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("canceling absent work created a journal")
	}
	invalid := []byte("invalid saved queue")
	if err := os.WriteFile(path, invalid, 0600); err != nil {
		t.Fatal(err)
	}
	if err := CancelPendingJournal(path); err == nil {
		t.Fatal("invalid journal was silently accepted")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != string(invalid) {
		t.Fatalf("corrupt journal was overwritten: %q %v", data, err)
	}
}
