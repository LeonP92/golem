package db

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestAcquireLeaseGrantsAndFences(t *testing.T) {
	gdb, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	token, acquired, err := AcquireLease(gdb, "scheduler:run-1", "holder-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("first acquire: token=%d acquired=%t err=%v", token, acquired, err)
	}
	if token != 1 {
		t.Errorf("first token = %d, want 1", token)
	}

	// A second holder is refused while the lease is valid.
	if _, acquired, err = AcquireLease(gdb, "scheduler:run-1", "holder-b", time.Minute); err != nil || acquired {
		t.Errorf("contending acquire: acquired=%t err=%v, want refused", acquired, err)
	}

	// The same holder re-acquiring bumps the token — a resumed process must
	// not reuse a token its crashed incarnation might still be acting on.
	token2, acquired, err := AcquireLease(gdb, "scheduler:run-1", "holder-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("re-acquire: %v", err)
	}
	if token2 <= token {
		t.Errorf("re-acquire token = %d, want > %d", token2, token)
	}
}

func TestAcquireLeaseStealsOnlyAfterExpiry(t *testing.T) {
	gdb, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, _, err := AcquireLease(gdb, "scheduler:run-2", "holder-a", -time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	token, acquired, err := AcquireLease(gdb, "scheduler:run-2", "holder-b", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("steal after expiry: acquired=%t err=%v", acquired, err)
	}

	// The stolen-from holder's token is now stale: CheckFencing is what
	// stops it acting after the newer holder started.
	ok, err := CheckFencing(gdb, "scheduler:run-2", token-1)
	if err != nil {
		t.Fatalf("CheckFencing: %v", err)
	}
	if ok {
		t.Error("stale token passed fencing check")
	}
	if ok, _ := CheckFencing(gdb, "scheduler:run-2", token); !ok {
		t.Error("current token failed fencing check")
	}
}

func TestRenewLeaseOnlyWhileHeld(t *testing.T) {
	gdb, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	token, acquired, err := AcquireLease(gdb, "outbox", "holder-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire: %v", err)
	}
	ok, err := RenewLease(gdb, "outbox", "holder-a", token, time.Minute)
	if err != nil || !ok {
		t.Errorf("renew while held: ok=%t err=%v", ok, err)
	}
	if ok, _ := RenewLease(gdb, "outbox", "holder-a", token+99, time.Minute); ok {
		t.Error("renew with a stale token succeeded")
	}
	if ok, _ := RenewLease(gdb, "outbox", "holder-b", token, time.Minute); ok {
		t.Error("renew by a non-holder succeeded")
	}
}

func TestReleaseLeaseRequiresHolderAndToken(t *testing.T) {
	gdb, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	token, _, err := AcquireLease(gdb, "outbox", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := ReleaseLease(gdb, "outbox", "holder-b", token); err == nil {
		t.Error("release by a non-holder succeeded")
	}
	if err := ReleaseLease(gdb, "outbox", "holder-a", token+1); err == nil {
		t.Error("release with a stale token succeeded")
	}
	if err := ReleaseLease(gdb, "outbox", "holder-a", token); err != nil {
		t.Fatalf("release by the holder: %v", err)
	}
	// After release, another holder acquires immediately.
	next, acquired, _ := AcquireLease(gdb, "outbox", "holder-b", time.Minute)
	if !acquired {
		t.Error("acquire after release was refused")
	}
	// And the token keeps climbing: a release must not reset fencing.
	if next <= token {
		t.Errorf("token after release = %d, want > %d", next, token)
	}
}

// Acquiring while another goroutine writes must not fail with
// SQLITE_BUSY (seen in the sandbox with the old read-then-write form).
func TestAcquireLeaseUnderConcurrentWrites(t *testing.T) {
	gdb, err := Open(filepath.Join(t.TempDir(), "busy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s, _ := gdb.DB(); _ = s.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 300; i++ {
			gdb.Transaction(func(tx *gorm.DB) error {
				return tx.Create(&Ticket{ID: fmt.Sprintf("t%d", i), RepoRemote: "r", Phase: "closed"}).Error
			})
		}
	}()
	for i := 0; i < 300; i++ {
		if _, _, err := AcquireLease(gdb, fmt.Sprintf("lane-%d", i%5), "h", time.Minute); err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
	}
	<-done
}
