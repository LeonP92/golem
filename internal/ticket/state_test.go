package ticket

import (
	"path/filepath"
	"testing"
)

func TestNewSetsDefaults(t *testing.T) {
	s := New("t1", "test description", false)
	if s.ID != "t1" {
		t.Errorf("ID = %q, want t1", s.ID)
	}
	if s.Description != "test description" {
		t.Errorf("Description = %q, want %q", s.Description, "test description")
	}
	if s.Phase != PhaseBrainstorm {
		t.Errorf("Phase = %q, want %q", s.Phase, PhaseBrainstorm)
	}
	if s.Trivial {
		t.Error("Trivial should be false")
	}
}

func TestNewTrivialSkipsBrainstorm(t *testing.T) {
	s := New("t2", "trivial task", true)
	if s.Phase != PhasePlan {
		t.Errorf("trivial ticket Phase = %q, want %q (skips brainstorm per spec)", s.Phase, PhasePlan)
	}
}

func TestSaveAndLoadPreservesExpectedLines(t *testing.T) {
	dir := t.TempDir()
	s := New("t4", "", false)
	s.CurrentStepExpectedLines = 15
	if err := s.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.CurrentStepExpectedLines != 15 {
		t.Errorf("CurrentStepExpectedLines = %d, want 15", loaded.CurrentStepExpectedLines)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := New("t3", "implementation task", false)
	s.Branch = "ticket/t3"
	s.WorktreePath = filepath.Join(dir, "worktree")
	s.Phase = PhaseImplement

	if err := s.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ID != s.ID || loaded.Branch != s.Branch || loaded.Phase != s.Phase {
		t.Errorf("loaded = %+v, want %+v", loaded, s)
	}
}
