package blog

import "testing"

func TestNewEntrySetsFields(t *testing.T) {
	e := NewEntry("developer", TypeStatus, "committed step 1")
	if e.Role != "developer" {
		t.Errorf("Role = %q, want developer", e.Role)
	}
	if e.Type != TypeStatus {
		t.Errorf("Type = %q, want %q", e.Type, TypeStatus)
	}
	if e.Message != "committed step 1" {
		t.Errorf("Message = %q, want %q", e.Message, "committed step 1")
	}
	if e.Timestamp.IsZero() {
		t.Error("Timestamp should not be zero")
	}
}
