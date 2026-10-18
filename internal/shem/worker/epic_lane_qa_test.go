package worker

// QA hardening pass over the epic merge lane: complicated ticket shapes an
// experienced tester would throw at the feature. Wide + deep + diamond
// dependency graphs, children finishing at the same instant, repeated
// multi-file conflicts (including a shallow fix that bounces again),
// tester failures after a conflict resolution, binary/rename/delete/large
// diffs, and pause/resume with an integrated sibling. Same full-stack
// harness as epic_lane_e2e_test.go: real orchestrator over in-memory
// SQLite, real executor stages, real plain-git forge; only the model is
// stubbed.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
)

// laneQABase returns the ref a dependent child must build on: the run's
// current integration target, fetched locally when pushes are on.
func laneQABase(t *testing.T, remote, repoPath, targetRef string, pushEnabled bool) string {
	t.Helper()
	if !pushEnabled {
		return laneGit(t, repoPath, "rev-parse", "refs/heads/"+targetRef)
	}
	laneGit(t, repoPath, "fetch", "-q", remote, "refs/heads/"+targetRef+":refs/golem-qa/base")
	return "refs/golem-qa/base"
}

func laneQAChildID(epicID, key string) string {
	return fmt.Sprintf("epic-%s-%s", epicID[:8], strings.ToLower(key))
}

func laneQAReceipt(t *testing.T, stack *laneStack, epicID, key string) string {
	t.Helper()
	var child db.Ticket
	if err := stack.gdb.First(&child, "id = ?", laneQAChildID(epicID, key)).Error; err != nil {
		t.Fatal(err)
	}
	return child.EpicIntegratedSHA
}

// laneQAFinish claims, scaffolds, works and finishes one child.
func laneQAFinish(t *testing.T, stack *laneStack, ex *GolemExecutor, cfg *config.Config, bin, epicID, key, repoPath, baseRef string) {
	t.Helper()
	claim, dir := stack.laneChildClaims(t, bin, epicID, key, repoPath, baseRef)
	if err := ex.finishWorkPhase(context.Background(), cfg, stack.shem, claim, repoPath, dir, "Implementation", claim.Branch); err != nil {
		t.Fatalf("finish %s: %v", key, err)
	}
}

// A ten-node epic that is wide (four roots), deep (a six-link chain) and
// diamond (D needs both B and C). Readiness gating, base pinning on the
// moving target and final content are all checked.
func TestEpicLaneQA_WideDeepDiamond(t *testing.T) {
	remote, repoPath := laneRepos(t, true)
	cfg := laneConfig(remote, repoPath, false)
	stack := newLaneStack(t, remote)
	bin := buildLaneCLI(t)
	runCLI(t, bin, "init", "--repo", repoPath, "--backend", "claude-code")
	ex := laneExecutor()

	keys := []string{"A", "B", "C", "D", "E", "F", "G", "P1", "P2", "P3"}
	nodes := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		nodes = append(nodes, laneNode(k, "node "+k))
	}
	epicID := stack.startLaneEpic(remote, nodes, []map[string]any{
		{"predecessor": "A", "successor": "B", "reason": "B on A"},
		{"predecessor": "A", "successor": "C", "reason": "C on A"},
		{"predecessor": "B", "successor": "D", "reason": "diamond left"},
		{"predecessor": "C", "successor": "D", "reason": "diamond right"},
		{"predecessor": "D", "successor": "E", "reason": "chain"},
		{"predecessor": "E", "successor": "F", "reason": "chain"},
		{"predecessor": "F", "successor": "G", "reason": "chain"},
	})
	targetRef := "epic/" + epicID

	// Gating: dependents are unclaimable until every predecessor is merged.
	if _, err := stack.shem.ClaimTicket(laneQAChildID(epicID, "B")); err == nil {
		t.Fatal("claimed B before A integrated")
	}
	if _, err := stack.shem.ClaimTicket(laneQAChildID(epicID, "G")); err == nil {
		t.Fatal("claimed deep node G before the chain integrated")
	}

	laneQAFinish(t, stack, ex, cfg, bin, epicID, "A", repoPath, "main")
	// The wide roots are independent: claimable from the start, base main.
	for _, k := range []string{"P1", "P2", "P3"} {
		laneQAFinish(t, stack, ex, cfg, bin, epicID, k, repoPath, "main")
	}
	laneQAFinish(t, stack, ex, cfg, bin, epicID, "B", repoPath, laneQABase(t, remote, repoPath, targetRef, true))
	// Diamond: one predecessor merged is not enough for D.
	if _, err := stack.shem.ClaimTicket(laneQAChildID(epicID, "D")); err == nil {
		t.Fatal("claimed D with only one diamond side integrated")
	}
	laneQAFinish(t, stack, ex, cfg, bin, epicID, "C", repoPath, laneQABase(t, remote, repoPath, targetRef, true))
	for _, k := range []string{"D", "E", "F", "G"} {
		laneQAFinish(t, stack, ex, cfg, bin, epicID, k, repoPath, laneQABase(t, remote, repoPath, targetRef, true))
	}

	if got := laneEpicState(t, stack.gdb, epicID); got != "awaiting-final-review" {
		t.Fatalf("run state = %q, want awaiting-final-review", got)
	}
	for _, k := range keys {
		if body := laneGit(t, "", "--git-dir", remote, "show", targetRef+":"+k+".go"); !strings.Contains(body, "// "+k) {
			t.Fatalf("target missing %s's commit: %q", k, body)
		}
		if laneQAReceipt(t, stack, epicID, k) == "" {
			t.Fatalf("child %s has no integration receipt", k)
		}
	}
	// Merge order on the target follows the chain: A before B before D ...
	log := laneGit(t, "", "--git-dir", remote, "log", "--format=%s", targetRef)
	chain := []string{"(A)", "(B)", "(D)", "(E)", "(F)", "(G)"}
	prev := len(log) + 1
	for _, mark := range chain {
		idx := strings.LastIndex(log, mark)
		if idx < 0 {
			t.Fatalf("target log missing merge %s:\n%s", mark, log)
		}
		if idx > prev {
			t.Fatalf("merge %s landed out of order:\n%s", mark, log)
		}
		prev = idx
	}
}

// Four children with no dependencies finish at the same instant. The gate
// grants one slot, the losers wait, and assigned reconciliation lands every
// commit exactly once. Run under -race to catch data races in the storm.
func TestEpicLaneQA_ConcurrentFinishes(t *testing.T) {
	remote, repoPath := laneRepos(t, true)
	cfg := laneConfig(remote, repoPath, false)
	stack := newLaneStack(t, remote)
	bin := buildLaneCLI(t)
	runCLI(t, bin, "init", "--repo", repoPath, "--backend", "claude-code")
	ctx := context.Background()
	ex := laneExecutor()

	keys := []string{"W", "X", "Y", "Z"}
	nodes := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		nodes = append(nodes, laneNode(k, "node "+k))
	}
	epicID := stack.startLaneEpic(remote, nodes, nil)
	targetRef := "epic/" + epicID

	claims := map[string]*client.ClaimResponse{}
	dirs := map[string]string{}
	for _, k := range keys {
		claim, dir := stack.laneChildClaims(t, bin, epicID, k, repoPath, "main")
		claims[k] = claim
		dirs[k] = dir
	}

	var wg sync.WaitGroup
	errs := make([]error, len(keys))
	for i, k := range keys {
		wg.Add(1)
		go func(i int, k string) {
			defer wg.Done()
			errs[i] = ex.finishWorkPhase(ctx, cfg, stack.shem, claims[k], repoPath, dirs[k], "Implementation", claims[k].Branch)
		}(i, k)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent finish %s: %v", keys[i], err)
		}
	}

	// Reconcile until every receipt lands.
	deadline := time.Now().Add(30 * time.Second)
	for {
		missing := []string{}
		for _, k := range keys {
			if laneQAReceipt(t, stack, epicID, k) == "" {
				missing = append(missing, k)
			}
		}
		if len(missing) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("receipts missing after reconciliation: %v", missing)
		}
		for _, k := range missing {
			ex.IntegrateAssigned(ctx, cfg, stack.shem, claims[k], repoPath, claims[k].Branch)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if got := laneEpicState(t, stack.gdb, epicID); got != "awaiting-final-review" {
		t.Fatalf("run state = %q, want awaiting-final-review", got)
	}
	head := laneTargetSHA(t, remote, repoPath, targetRef, true)
	for _, k := range keys {
		if body := laneGit(t, "", "--git-dir", remote, "show", targetRef+":"+k+".go"); !strings.Contains(body, "// "+k) {
			t.Fatalf("target missing %s's commit after the storm: %q", k, body)
		}
		receipt := laneQAReceipt(t, stack, epicID, k)
		if receipt == "" {
			t.Fatalf("child %s has no receipt", k)
		}
		// The recorded merge is on the target's first-parent history.
		laneGit(t, "", "--git-dir", remote, "merge-base", "--is-ancestor", receipt, head)
	}
	// Every child contributed exactly one work commit. The merge count
	// depends on a race: a finisher may pre-create the target ref at the
	// base (4 merges) or leave it to the lane's first merge (3 merges).
	count := laneGit(t, "", "--git-dir", remote, "rev-list", "--count", targetRef, "^main")
	if count != "7" && count != "8" {
		t.Fatalf("target carries %s commits over main, want 7 or 8 (4 children + 3-4 merges)", count)
	}
}

// A conflicting child whose first "fix" does not merge the target bounces
// again with a narrower conflict; the proper merge resolution lands. The
// attempt counter tracks both bounces and the run still completes.
func TestEpicLaneQA_RepeatedMultiFileConflict(t *testing.T) {
	remote, repoPath := laneRepos(t, true)
	cfg := laneConfig(remote, repoPath, false)
	stack := newLaneStack(t, remote)
	bin := buildLaneCLI(t)
	runCLI(t, bin, "init", "--repo", repoPath, "--backend", "claude-code")
	ctx := context.Background()
	ex := laneExecutor()

	// Seed three shared files on main before the run starts.
	for _, f := range []string{"f1.txt", "f2.txt", "f3.txt"} {
		if err := os.WriteFile(filepath.Join(repoPath, f), []byte("base "+f+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	laneGit(t, repoPath, "add", ".")
	laneGit(t, repoPath, "commit", "-qm", "seed shared files")
	laneGit(t, repoPath, "push", "-q", "origin", "main")

	epicID := stack.startLaneEpic(remote,
		[]map[string]any{laneNode("X", "first"), laneNode("Y", "second")}, nil)
	targetRef := "epic/" + epicID
	yID := laneQAChildID(epicID, "Y")

	claimX, dirX := stack.laneChildClaims(t, bin, epicID, "X", repoPath, "main")
	wtX := filepath.Join(dirX, "worktree")
	for _, f := range []string{"f1.txt", "f2.txt", "f3.txt"} {
		if err := os.WriteFile(filepath.Join(wtX, f), []byte("x "+f+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	laneGit(t, wtX, "add", ".")
	laneGit(t, wtX, "commit", "-qm", "x rewrites all three")
	if err := ex.finishWorkPhase(ctx, cfg, stack.shem, claimX, repoPath, dirX, "Implementation", claimX.Branch); err != nil {
		t.Fatalf("finish X: %v", err)
	}

	claimY, dirY := stack.laneChildClaims(t, bin, epicID, "Y", repoPath, "main")
	wtY := filepath.Join(dirY, "worktree")
	for _, f := range []string{"f1.txt", "f2.txt", "f3.txt"} {
		if err := os.WriteFile(filepath.Join(wtY, f), []byte("y "+f+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	laneGit(t, wtY, "add", ".")
	laneGit(t, wtY, "commit", "-qm", "y rewrites all three")
	if err := ex.finishWorkPhase(ctx, cfg, stack.shem, claimY, repoPath, dirY, "Implementation", claimY.Branch); err != nil {
		t.Fatalf("finish Y: %v", err)
	}
	if got := laneTicketPhase(t, stack.gdb, yID); got != "revising" {
		t.Fatalf("Y phase after first conflict = %q, want revising", got)
	}
	var child db.Ticket
	stack.gdb.First(&child, "id = ?", yID)
	if child.PRFixAttempts != 1 {
		t.Fatalf("attempts after first bounce = %d, want 1", child.PRFixAttempts)
	}

	// Shallow fix: rewrite files WITHOUT merging the target, taking the
	// target's content for f2/f3 and a combined f1. The lane merge base is
	// still main, so f1 conflicts again - but f2/f3 no longer do.
	laneGit(t, wtY, "fetch", "-q", remote, "refs/heads/"+targetRef+":refs/golem-qa/target")
	for f, content := range map[string]string{
		"f1.txt": "x f1.txt\ny f1.txt\n",
		"f2.txt": "x f2.txt\n",
		"f3.txt": "x f3.txt\n",
	} {
		if err := os.WriteFile(filepath.Join(wtY, f), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	laneGit(t, wtY, "add", ".")
	laneGit(t, wtY, "commit", "-qm", "shallow fix without merging the target")
	if err := ex.finishWorkPhase(ctx, cfg, stack.shem, claimY, repoPath, dirY, "Implementation", claimY.Branch); err != nil {
		t.Fatalf("refinish Y (shallow): %v", err)
	}
	stack.gdb.First(&child, "id = ?", yID)
	if child.Phase != "revising" || child.PRFixAttempts != 2 {
		t.Fatalf("shallow fix did not bounce again: phase=%s attempts=%d", child.Phase, child.PRFixAttempts)
	}

	// Proper fix: merge the target into the branch and resolve f1.
	laneGit(t, wtY, "fetch", remote, "refs/heads/"+targetRef)
	mergeCmd := exec.Command("git", "merge", "--no-edit", "FETCH_HEAD")
	mergeCmd.Dir = wtY
	_ = mergeCmd.Run()
	if err := os.WriteFile(filepath.Join(wtY, "f1.txt"), []byte("x f1.txt\ny f1.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	laneGit(t, wtY, "add", ".")
	laneGit(t, wtY, "commit", "-qm", "merge the target and resolve f1")
	if err := ex.finishWorkPhase(ctx, cfg, stack.shem, claimY, repoPath, dirY, "Implementation", claimY.Branch); err != nil {
		t.Fatalf("refinish Y (proper): %v", err)
	}
	stack.gdb.First(&child, "id = ?", yID)
	if child.Phase != "ready-for-review" || child.EpicIntegratedSHA == "" || child.PRFixAttempts != 2 {
		t.Fatalf("proper fix did not integrate: %+v", child)
	}
	if got := laneEpicState(t, stack.gdb, epicID); got != "awaiting-final-review" {
		t.Fatalf("run state = %q, want awaiting-final-review", got)
	}
	for f, want := range map[string]string{
		"f1.txt": "x f1.txt\ny f1.txt",
		"f2.txt": "x f2.txt",
		"f3.txt": "x f3.txt",
	} {
		if body := laneGit(t, "", "--git-dir", remote, "show", targetRef+":"+f); body != want {
			t.Fatalf("%s on target = %q, want %q", f, body, want)
		}
	}
}

// One child carries the awkward diff shapes at once: a rename, a deletion,
// a binary file with NUL bytes, a multi-megabyte file and a fan of small
// files. Everything must survive the lane byte-for-byte.
func TestEpicLaneQA_BinaryRenameDeleteLargeDiff(t *testing.T) {
	remote, repoPath := laneRepos(t, true)
	cfg := laneConfig(remote, repoPath, false)
	stack := newLaneStack(t, remote)
	bin := buildLaneCLI(t)
	runCLI(t, bin, "init", "--repo", repoPath, "--backend", "claude-code")
	ex := laneExecutor()

	if err := os.WriteFile(filepath.Join(repoPath, "old.txt"), []byte("delete me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	laneGit(t, repoPath, "add", ".")
	laneGit(t, repoPath, "commit", "-qm", "seed a file to delete")
	laneGit(t, repoPath, "push", "-q", "origin", "main")

	epicID := stack.startLaneEpic(remote, []map[string]any{laneNode("A", "awkward diff")}, nil)
	targetRef := "epic/" + epicID

	claim, dir := stack.laneChildClaims(t, bin, epicID, "A", repoPath, "main")
	wt := filepath.Join(dir, "worktree")
	laneGit(t, wt, "mv", "README.md", "GUIDE.md")
	laneGit(t, wt, "rm", "-q", "old.txt")
	binary := make([]byte, 256)
	for i := range binary {
		binary[i] = byte(i)
	}
	if err := os.WriteFile(filepath.Join(wt, "bin.dat"), binary, 0o644); err != nil {
		t.Fatal(err)
	}
	var big strings.Builder
	for i := 0; i < 200000; i++ {
		fmt.Fprintf(&big, "line %06d of the large generated file\n", i)
	}
	if err := os.WriteFile(filepath.Join(wt, "big.txt"), []byte(big.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		name := fmt.Sprintf("pkg/mod%02d.go", i)
		if err := os.MkdirAll(filepath.Join(wt, "pkg"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wt, name), []byte(fmt.Sprintf("package pkg\n\n// mod %d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	laneGit(t, wt, "add", ".")
	laneGit(t, wt, "commit", "-qm", "rename, delete, binary, large, many files")
	if err := ex.finishWorkPhase(context.Background(), cfg, stack.shem, claim, repoPath, dir, "Implementation", claim.Branch); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if got := laneEpicState(t, stack.gdb, epicID); got != "awaiting-final-review" {
		t.Fatalf("run state = %q, want awaiting-final-review", got)
	}

	names := laneGit(t, "", "--git-dir", remote, "ls-tree", "-r", "--name-only", targetRef)
	for _, gone := range []string{"README.md", "old.txt"} {
		for _, line := range strings.Split(names, "\n") {
			if line == gone {
				t.Fatalf("%s survived the lane", gone)
			}
		}
	}
	for _, want := range []string{"GUIDE.md", "bin.dat", "big.txt", "pkg/mod00.go", "pkg/mod29.go", "A.go"} {
		if !strings.Contains(names, want) {
			t.Fatalf("target tree missing %s:\n%s", want, names)
		}
	}
	// Byte-exact checks for the binary and the large file, straight out of
	// the bare remote.
	readBlob := func(path string) []byte {
		cmd := exec.Command("git", "--git-dir", remote, "show", targetRef+":"+path)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return out
	}
	if got := readBlob("bin.dat"); !bytes.Equal(got, binary) {
		t.Fatalf("binary mangled: %d bytes", len(got))
	}
	if got := readBlob("big.txt"); !bytes.Equal(got, []byte(big.String())) {
		t.Fatalf("large file truncated: %d bytes, want %d", len(got), big.Len())
	}
	if got := readBlob("GUIDE.md"); string(got) != "lane fixture\n" {
		t.Fatalf("renamed file content = %q", got)
	}
}

// Pausing with one child integrated and more claimed work outstanding: the
// finished child waits unmerged, claims stop, and resume reconciles the
// backlog to completion.
func TestEpicLaneQA_PauseWithIntegratedSibling(t *testing.T) {
	remote, repoPath := laneRepos(t, true)
	cfg := laneConfig(remote, repoPath, false)
	stack := newLaneStack(t, remote)
	bin := buildLaneCLI(t)
	runCLI(t, bin, "init", "--repo", repoPath, "--backend", "claude-code")
	ctx := context.Background()
	ex := laneExecutor()

	epicID := stack.startLaneEpic(remote,
		[]map[string]any{laneNode("A", "one"), laneNode("B", "two"), laneNode("C", "three")}, nil)

	laneQAFinish(t, stack, ex, cfg, bin, epicID, "A", repoPath, "main")
	if laneQAReceipt(t, stack, epicID, "A") == "" {
		t.Fatal("A did not integrate before the pause")
	}
	claimB, dirB := stack.laneChildClaims(t, bin, epicID, "B", repoPath, "main")

	if code := stack.human("POST", "/api/epics/"+epicID+"/pause", nil); code != http.StatusNoContent {
		t.Fatalf("pause=%d", code)
	}
	// Claims stop while paused.
	if _, err := stack.shem.ClaimTicket(laneQAChildID(epicID, "C")); err == nil {
		t.Fatal("claimed a new child while the run is paused")
	}
	// Already-claimed work finishes but does not merge.
	if err := ex.finishWorkPhase(ctx, cfg, stack.shem, claimB, repoPath, dirB, "Implementation", claimB.Branch); err != nil {
		t.Fatalf("finish B paused: %v", err)
	}
	if got := laneTicketPhase(t, stack.gdb, laneQAChildID(epicID, "B")); got != "ready-for-review" {
		t.Fatalf("paused B phase = %q, want ready-for-review", got)
	}
	if laneQAReceipt(t, stack, epicID, "B") != "" {
		t.Fatal("paused B integrated")
	}
	if got := laneEpicState(t, stack.gdb, epicID); got != "paused" {
		t.Fatalf("run state = %q, want paused", got)
	}

	if code := stack.human("POST", "/api/epics/"+epicID+"/resume", nil); code != http.StatusNoContent {
		t.Fatalf("resume=%d", code)
	}
	ex.IntegrateAssigned(ctx, cfg, stack.shem, claimB, repoPath, claimB.Branch)
	if laneQAReceipt(t, stack, epicID, "B") == "" {
		t.Fatal("resumed B did not integrate")
	}
	laneQAFinish(t, stack, ex, cfg, bin, epicID, "C", repoPath, "main")
	if got := laneEpicState(t, stack.gdb, epicID); got != "awaiting-final-review" {
		t.Fatalf("run state = %q, want awaiting-final-review", got)
	}
}

// Conflict bounce followed by a tester failure on the resolved head: the
// two feedback paths compose, the verdict stays pinned to each head, and
// the eventual fix integrates. Attempts: 1 conflict + 1 tester failure.
func TestEpicLaneQA_ConflictThenTesterFailThenRecover(t *testing.T) {
	remote, repoPath := laneRepos(t, true)
	cfg := laneConfig(remote, repoPath, false)
	stack := newLaneStack(t, remote)
	bin := buildLaneCLI(t)
	runCLI(t, bin, "init", "--repo", repoPath, "--backend", "claude-code")
	ctx := context.Background()
	ex := laneExecutor()

	epicID := stack.startLaneEpic(remote,
		[]map[string]any{laneNode("X", "one"), laneNode("Y", "two")}, nil)
	targetRef := "epic/" + epicID
	yID := laneQAChildID(epicID, "Y")

	claimX, dirX := stack.laneChildClaims(t, bin, epicID, "X", repoPath, "main")
	wtX := filepath.Join(dirX, "worktree")
	if err := os.WriteFile(filepath.Join(wtX, "shared.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	laneGit(t, wtX, "add", ".")
	laneGit(t, wtX, "commit", "-qm", "x writes shared")
	if err := ex.finishWorkPhase(ctx, cfg, stack.shem, claimX, repoPath, dirX, "Implementation", claimX.Branch); err != nil {
		t.Fatalf("finish X: %v", err)
	}

	claimY, dirY := stack.laneChildClaims(t, bin, epicID, "Y", repoPath, "main")
	wtY := filepath.Join(dirY, "worktree")
	if err := os.WriteFile(filepath.Join(wtY, "shared.txt"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	laneGit(t, wtY, "add", ".")
	laneGit(t, wtY, "commit", "-qm", "y writes shared")
	if err := ex.finishWorkPhase(ctx, cfg, stack.shem, claimY, repoPath, dirY, "Implementation", claimY.Branch); err != nil {
		t.Fatalf("finish Y: %v", err)
	}
	var child db.Ticket
	stack.gdb.First(&child, "id = ?", yID)
	if child.Phase != "revising" || child.PRFixAttempts != 1 {
		t.Fatalf("conflict did not bounce: %+v", child)
	}

	// Resolve against the target, then have the tester fail that head.
	laneGit(t, wtY, "fetch", remote, "refs/heads/"+targetRef)
	mergeCmd := exec.Command("git", "merge", "--no-edit", "FETCH_HEAD")
	mergeCmd.Dir = wtY
	_ = mergeCmd.Run()
	if err := os.WriteFile(filepath.Join(wtY, "shared.txt"), []byte("x\ny\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	laneGit(t, wtY, "add", ".")
	laneGit(t, wtY, "commit", "-qm", "resolve with the target")
	failedHead := laneGit(t, wtY, "rev-parse", "HEAD")
	ex.Agent.Adapter = laneAdapter{Mock: &agentrunner.Mock{}, verdict: "FAIL"}
	if err := ex.finishWorkPhase(ctx, cfg, stack.shem, claimY, repoPath, dirY, "Implementation", claimY.Branch); err != nil {
		t.Fatalf("refinish Y (fail verdict): %v", err)
	}
	stack.gdb.First(&child, "id = ?", yID)
	if child.Phase != "revising" || child.PRFixAttempts != 2 {
		t.Fatalf("tester failure after conflict did not bounce: %+v", child)
	}
	if _, err := stack.shem.CheckIntegrationGate(epicID, "Y", failedHead); err == nil {
		t.Fatal("failed head got an integration lease")
	}

	// The fix is a new head; its own verdict passes and the lane merges it.
	revision, err := stack.shem.ClaimRevision(yID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtY, "fix.txt"), []byte("fixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	laneGit(t, wtY, "add", ".")
	laneGit(t, wtY, "commit", "-qm", "fix the tester failure")
	runCLI(t, bin, "ticket", "advance", "--repo", repoPath, "--ticket", yID, "--to", "review")
	ex.Agent.Adapter = laneAdapter{Mock: &agentrunner.Mock{}}
	if err := ex.finishWorkPhase(ctx, cfg, stack.shem, revision, repoPath, dirY, "Revision", revision.Branch); err != nil {
		t.Fatalf("refinish Y (fixed): %v", err)
	}
	stack.gdb.First(&child, "id = ?", yID)
	if child.Phase != "ready-for-review" || child.EpicIntegratedSHA == "" || child.PRFixAttempts != 2 {
		t.Fatalf("fixed child did not integrate: %+v", child)
	}
	fixedHead := laneGit(t, wtY, "rev-parse", "HEAD")
	var verdicts []db.TesterVerdict
	stack.gdb.Where("epic_id = ? AND epic_node_key = ?", epicID, "Y").Order("id").Find(&verdicts)
	if len(verdicts) != 3 || verdicts[0].Verdict != "pass" || verdicts[1].HeadSHA != failedHead || verdicts[1].Verdict != "fail" || verdicts[2].HeadSHA != fixedHead || verdicts[2].Verdict != "pass" {
		t.Fatalf("verdict history = %+v", verdicts)
	}
	if got := laneEpicState(t, stack.gdb, epicID); got != "awaiting-final-review" {
		t.Fatalf("run state = %q, want awaiting-final-review", got)
	}
	if body := laneGit(t, "", "--git-dir", remote, "show", targetRef+":shared.txt"); body != "x\ny" {
		t.Fatalf("resolved content = %q", body)
	}
}

// The integration ref create is check-then-act: children that finish
// together both see it missing and both push. The loser must treat the
// now-existing ref as success, not report a spurious failure. This stress
// reproduces the race directly; pre-fix rounds fail with "cannot lock ref".
func TestEnsureRemoteBaseRef_ConcurrentCreate(t *testing.T) {
	root, err := os.MkdirTemp("", "golem-qa-baseref-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	laneGit(t, seed, "init", "-q", "-b", "main")
	laneGit(t, seed, "config", "user.email", "qa@example.com")
	laneGit(t, seed, "config", "user.name", "qa")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	laneGit(t, seed, "add", ".")
	laneGit(t, seed, "commit", "-qm", "initial")
	remote := filepath.Join(root, "remote")
	laneGit(t, "", "clone", "-q", "--bare", seed, remote)
	sha := laneGit(t, seed, "rev-parse", "main")
	repoPath := filepath.Join(root, "work", "repo")

	for round := 0; round < 25; round++ {
		ref := fmt.Sprintf("epic/round-%02d", round)
		var wg sync.WaitGroup
		errs := make([]error, 4)
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				racerRepo := repoPath
				if round%2 != 0 {
					// Separate mirrors model independent shems racing the same remote.
					racerRepo = filepath.Join(root, "work", fmt.Sprintf("repo-%d", g))
				}
				errs[g] = ensureRemoteBaseRef(context.Background(), racerRepo, remote, "main", ref, sha)
			}(g)
		}
		wg.Wait()
		for g, err := range errs {
			if err != nil {
				t.Fatalf("round %d racer %d: %v", round, g, err)
			}
		}
		if got := laneGit(t, "", "--git-dir", remote, "rev-parse", "refs/heads/"+ref); got != sha {
			t.Fatalf("round %d: ref at %s, want %s", round, got, sha)
		}
	}

	// A genuine failure — the remote is gone — must still report, not be
	// swallowed by the re-check.
	if err := ensureRemoteBaseRef(context.Background(), repoPath,
		filepath.Join(root, "missing"), "main", "epic/nope", sha); err == nil {
		t.Fatal("a dead remote reported success")
	}
}
