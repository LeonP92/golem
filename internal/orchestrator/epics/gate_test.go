package epics

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

// gateFixture is a running epic whose child T1 is ready-for-review with a
// tester pass for head1.
type gateFixture struct {
	gdb   *gorm.DB
	epic  *db.Epic
	child *db.Ticket
	now   time.Time
}

func newGateFixture(t *testing.T, autoMerge bool) *gateFixture {
	t.Helper()
	gdb, _ := db.Open(":memory:")
	e, err := Create(gdb, CreateParams{
		RepoRemote: "https://example.com/org/repo", Title: "x", SpecMarkdown: "s",
		AutoMergeChildren: autoMerge,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rev, err := AddRevision(gdb, e.ID, validPlan(), "")
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	if err := Approve(gdb, e.ID, rev.Revision, rev.PlanHash, "leon"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := Start(gdb, e.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	var tk db.Ticket
	if err := gdb.First(&tk, "epic_id = ? AND epic_node_key = 'T1'", e.ID).Error; err != nil {
		t.Fatalf("child ticket: %v", err)
	}
	if err := gdb.Model(&tk).Update("phase", "ready-for-review").Error; err != nil {
		t.Fatalf("phase: %v", err)
	}
	tk.Phase = "ready-for-review"
	if e.TesterRequired {
		if err := RecordVerdict(gdb, db.TesterVerdict{
			EpicID: e.ID, EpicNodeKey: "T1", HeadSHA: "head1", Verdict: "pass",
		}); err != nil {
			t.Fatalf("verdict: %v", err)
		}
	}
	return &gateFixture{gdb: gdb, epic: e, child: &tk, now: time.Now()}
}

func (f *gateFixture) check(t *testing.T, nodeKey, headSHA string, at time.Time) Gate {
	t.Helper()
	g, err := GateIntegration(f.gdb, f.epic.ID, nodeKey, headSHA, 7, at)
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	return g
}

func TestGateGrantsOneSlotPerEpic(t *testing.T) {
	f := newGateFixture(t, true)

	// Two children finish at once: the first gate check wins the slot...
	g := f.check(t, "T1", "head1", f.now)
	if g.Outcome != GateGranted || g.TargetRef != f.epic.IntegrationRef {
		t.Fatalf("first check = %+v", g)
	}
	// ...and the second is told to wait, with when to come back.
	var t2 db.Ticket
	if err := f.gdb.First(&t2, "epic_id = ? AND epic_node_key = 'T2'", f.epic.ID).Error; err != nil {
		t.Fatalf("child T2: %v", err)
	}
	if err := f.gdb.Model(&t2).Update("phase", "ready-for-review").Error; err != nil {
		t.Fatalf("phase T2: %v", err)
	}
	if err := RecordVerdict(f.gdb, db.TesterVerdict{
		EpicID: f.epic.ID, EpicNodeKey: "T2", HeadSHA: "head2", Verdict: "pass",
	}); err != nil {
		t.Fatalf("verdict T2: %v", err)
	}
	g = f.check(t, "T2", "head2", f.now)
	if g.Outcome != GateWait || g.RetryAfter <= 0 || g.Reason == "" {
		t.Fatalf("second check = %+v, want wait", g)
	}

	// The receipt frees the lane; the waiting child merges next.
	if err := RecordIntegration(f.gdb, f.epic.ID, "T1", "merge1"); err != nil {
		t.Fatalf("receipt: %v", err)
	}
	g = f.check(t, "T2", "head2", f.now)
	if g.Outcome != GateGranted {
		t.Fatalf("check after receipt = %+v, want granted", g)
	}
}

func TestGateSlotExpiryAndRecheck(t *testing.T) {
	f := newGateFixture(t, true)
	if g := f.check(t, "T1", "head1", f.now); g.Outcome != GateGranted {
		t.Fatalf("first check = %+v", g)
	}
	// The holder crashed: after the TTL a fresh head (the tester passed it
	// meanwhile) takes the slot over.
	if err := RecordVerdict(f.gdb, db.TesterVerdict{
		EpicID: f.epic.ID, EpicNodeKey: "T1", HeadSHA: "head1b", Verdict: "pass",
	}); err != nil {
		t.Fatalf("verdict head1b: %v", err)
	}
	g := f.check(t, "T1", "head1b", f.now.Add(SlotTTL+time.Second))
	if g.Outcome != GateGranted {
		t.Fatalf("takeover after expiry = %+v, want granted", g)
	}
	// The same node re-checking before expiry refreshes its own slot rather
	// than waiting on itself.
	g = f.check(t, "T1", "head1b", f.now.Add(SlotTTL+2*time.Second))
	if g.Outcome != GateGranted {
		t.Fatalf("same-node re-check = %+v, want granted", g)
	}
}

func TestGatePreconditions(t *testing.T) {
	// A run that does not integrate children itself has no lane.
	f := newGateFixture(t, false)
	if _, err := GateIntegration(f.gdb, f.epic.ID, "T1", "head1", 7, f.now); err == nil {
		t.Fatal("gate opened for a run without auto-integrate")
	}

	f = newGateFixture(t, true)
	// A verdict for a DIFFERENT head is no verdict for this one.
	if _, err := GateIntegration(f.gdb, f.epic.ID, "T1", "other-head", 7, f.now); err == nil {
		t.Fatal("gate opened without a tester pass for the head")
	}
	// An integrated node is done, not granted: a retry after a crash.
	if err := RecordIntegration(f.gdb, f.epic.ID, "T1", "merge1"); err != nil {
		t.Fatalf("receipt: %v", err)
	}
	if g := f.check(t, "T1", "head1", f.now); g.Outcome != GateDone {
		t.Fatalf("integrated node = %+v, want done", g)
	}
	// A paused run waits rather than failing.
	if err := Pause(f.gdb, f.epic.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if g := f.check(t, "T1", "head1", f.now); g.Outcome != GateDone {
		t.Fatalf("integrated node on paused run = %+v, want done", g)
	}
}

func TestGateSlotReleasedWithoutReceipt(t *testing.T) {
	f := newGateFixture(t, true)
	if g := f.check(t, "T1", "head1", f.now); g.Outcome != GateGranted {
		t.Fatalf("first check = %+v", g)
	}
	// The lane ended without a receipt (a conflict, a moved head): the slot
	// is released explicitly, not left to the TTL.
	var slot db.EpicIntegrationSlot
	f.gdb.First(&slot, "epic_id = ?", f.epic.ID)
	f.gdb.Model(f.child).Update("assigned_shem", slot.ShemID)
	if err := ApplyIntegrationResult(f.gdb, slot, "", func(*gorm.DB, db.Ticket) error { return nil }); err != nil {
		t.Fatalf("release: %v", err)
	}
	var count int64
	f.gdb.Model(&db.EpicIntegrationSlot{}).Where("epic_id = ?", f.epic.ID).Count(&count)
	if count != 0 {
		t.Fatal("slot row survived the release")
	}
}

// Simultaneous callers may refresh their own node's lease, but two nodes
// must never both acquire the same epic's slot.
func TestGateConcurrentChildren(t *testing.T) {
	f := newGateFixture(t, true)
	sqlDB, err := f.gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := f.gdb.Model(&db.Ticket{}).Where("epic_id = ?", f.epic.ID).Update("phase", "ready-for-review").Error; err != nil {
		t.Fatal(err)
	}
	if err := RecordVerdict(f.gdb, db.TesterVerdict{EpicID: f.epic.ID, EpicNodeKey: "T2", HeadSHA: "head2", Verdict: "pass"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	granted := make(chan string, 16)
	for i := 0; i < 16; i++ {
		node, head := "T1", "head1"
		if i%2 != 0 {
			node, head = "T2", "head2"
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, err := GateIntegration(f.gdb, f.epic.ID, node, head, 7, f.now)
			if err != nil {
				t.Error(err)
				return
			}
			if g.Outcome == GateGranted {
				granted <- node
			}
		}()
	}
	wg.Wait()
	close(granted)
	winner := ""
	for node := range granted {
		if winner != "" && winner != node {
			t.Fatalf("both nodes acquired one slot: %s and %s", winner, node)
		}
		winner = node
	}
	if winner == "" {
		t.Fatal("neither child acquired the slot")
	}
}

// A late retry from one child must never release a sibling's lease.
func TestDuplicateReceiptPreservesSiblingLease(t *testing.T) {
	f := newGateFixture(t, true)
	if err := RecordIntegration(f.gdb, f.epic.ID, "T1", "merge1"); err != nil {
		t.Fatal(err)
	}
	slot := db.EpicIntegrationSlot{EpicID: f.epic.ID, NodeKey: "T2", HeadSHA: "head2", LeaseID: "sibling", ShemID: 7, ExpiresAt: f.now.Add(SlotTTL)}
	if err := f.gdb.Create(&slot).Error; err != nil {
		t.Fatal(err)
	}
	if err := RecordIntegration(f.gdb, f.epic.ID, "T1", "merge1"); err != nil {
		t.Fatal(err)
	}
	var got db.EpicIntegrationSlot
	if err := f.gdb.First(&got, "epic_id = ?", f.epic.ID).Error; err != nil || got.LeaseID != slot.LeaseID {
		t.Fatalf("sibling lease lost: %+v %v", got, err)
	}
}

func TestIntegrationResultFencesReplacedLease(t *testing.T) {
	f := newGateFixture(t, true)
	if err := f.gdb.Model(f.child).Update("assigned_shem", 7).Error; err != nil {
		t.Fatal(err)
	}
	first := f.check(t, "T1", "head1", f.now)
	// Same-head takeover still needs a different identity: a restarted holder
	// must not inherit the authority of a delayed result from the old process.
	if err := f.gdb.Model(&db.EpicIntegrationSlot{}).Where("epic_id = ?", f.epic.ID).Update("expires_at", f.now.Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	second := f.check(t, "T1", "head1", f.now)
	if first.LeaseID == second.LeaseID {
		t.Fatal("takeover reused old lease identity")
	}
	stale := db.EpicIntegrationSlot{EpicID: f.epic.ID, NodeKey: "T1", HeadSHA: "head1", ShemID: 7, LeaseID: first.LeaseID}
	called := false
	if err := ApplyIntegrationResult(f.gdb, stale, "", func(*gorm.DB, db.Ticket) error { called = true; return nil }); !errors.Is(err, ErrIntegrationConflict) || called {
		t.Fatalf("stale result applied: %v", err)
	}
	var cur db.EpicIntegrationSlot
	if err := f.gdb.First(&cur, "epic_id = ?", f.epic.ID).Error; err != nil || cur.LeaseID != second.LeaseID {
		t.Fatalf("new lease lost: %+v %v", cur, err)
	}
	// Applying an outcome and consuming its lease must roll back together.
	sentinel := errors.New("outbox write failed")
	if err := ApplyIntegrationResult(f.gdb, cur, "", func(*gorm.DB, db.Ticket) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	var count int64
	f.gdb.Model(&db.EpicIntegrationSlot{}).Where("epic_id = ? AND lease_id = ?", cur.EpicID, cur.LeaseID).Count(&count)
	if count != 1 {
		t.Fatal("failed outcome consumed its lease")
	}
}
