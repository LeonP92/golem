package ui_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/epics"
)

// An open run offers Cancel (posting to the API like the ticket Close
// button); a cancelled one shows who, why and no further controls.
func TestEpicDetailCancelControls(t *testing.T) {
	gdb, mux, cookie := setupEpicUITest(t)
	e, err := epics.Create(gdb, epics.CreateParams{RepoRemote: "https://github.com/org/repo", Title: "Doomed", SpecMarkdown: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := epics.AddRevision(gdb, e.ID, mustPlan(t), ""); err != nil {
		t.Fatal(err)
	}
	body := epicGet(t, mux, cookie, "/epics/"+e.ID).Body.String()
	if !strings.Contains(body, `hx-post="/api/epics/`+e.ID+`/cancel"`) || !strings.Contains(body, "Approve this plan") {
		t.Fatal("open run lacks the cancel or approve control")
	}

	now := time.Now()
	gdb.Model(&db.Epic{}).Where("id = ?", e.ID).Updates(map[string]any{
		"state": epics.StateCancelled, "cancel_reason": "wrong idea", "cancelled_by": "leon", "cancelled_at": &now})
	body = epicGet(t, mux, cookie, "/epics/"+e.ID).Body.String()
	if strings.Contains(body, "/cancel\"") || strings.Contains(body, "Approve this plan") || strings.Contains(body, "Generate plan") {
		t.Fatal("cancelled run still offers controls")
	}
	if !strings.Contains(body, "Cancelled by leon") || !strings.Contains(body, "wrong idea") {
		t.Fatal("cancelled run does not say who and why")
	}
}

// The list hides cancelled runs unless asked for them, and says how many
// it hid.
func TestEpicListHidesCancelled(t *testing.T) {
	gdb, mux, cookie := setupEpicUITest(t)
	for _, title := range []string{"Kept", "Dropped"} {
		e, err := epics.Create(gdb, epics.CreateParams{RepoRemote: "https://github.com/org/repo", Title: title, SpecMarkdown: "s"})
		if err != nil {
			t.Fatal(err)
		}
		if title == "Dropped" {
			gdb.Model(&db.Epic{}).Where("id = ?", e.ID).Update("state", epics.StateCancelled)
		}
	}
	body := epicGet(t, mux, cookie, "/epics").Body.String()
	if !strings.Contains(body, "Kept") || strings.Contains(body, "Dropped") || !strings.Contains(body, "1 cancelled") {
		t.Fatal("default list must hide cancelled runs and count them")
	}
	body = epicGet(t, mux, cookie, "/epics?state=cancelled").Body.String()
	if strings.Contains(body, "Kept") || !strings.Contains(body, "Dropped") {
		t.Fatal("cancelled filter must show only cancelled runs")
	}
}

func mustPlan(t *testing.T) epic.Plan {
	t.Helper()
	var p epic.Plan
	if err := json.Unmarshal([]byte(epicUITestPlan), &p); err != nil {
		t.Fatal(err)
	}
	return p
}
