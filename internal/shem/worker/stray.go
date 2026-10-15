package worker

import (
	"log"
	"os"
	"path/filepath"
)

// adoptStrayTicketFile moves name into ticketDir when the agent wrote it to
// the worktree's copy of the ticket directory instead. An agent that cd's into
// its worktree resolves the prompt's relative .golem/tickets/<id>/ path there,
// and the phase would then fail validation for a file it did write. The stray
// copy wins when the real one is missing or older (a re-run's revision). Log
// entries it emitted from there are appended to the real log too.
func adoptStrayTicketFile(ticketDir, name string) {
	stray := filepath.Join(ticketDir, "worktree", ".golem", "tickets", filepath.Base(ticketDir))
	want := filepath.Join(ticketDir, name)
	if si, err := os.Stat(filepath.Join(stray, name)); err == nil {
		wi, werr := os.Stat(want)
		if os.IsNotExist(werr) || (werr == nil && si.ModTime().After(wi.ModTime())) {
			if data, err := os.ReadFile(filepath.Join(stray, name)); err != nil {
				log.Printf("executor: reading stray %s: %v", name, err)
			} else if err := os.WriteFile(want, data, 0o644); err != nil { //nolint:gosec
				log.Printf("executor: adopting %s into %s: %v", name, ticketDir, err)
			} else {
				log.Printf("executor: adopted %s from the worktree into %s", name, ticketDir)
				_ = os.Remove(filepath.Join(stray, name))
			}
		}
	}
	entries, err := os.ReadFile(filepath.Join(stray, "log.jsonl"))
	if err != nil || len(entries) == 0 {
		return
	}
	f, err := os.OpenFile(filepath.Join(ticketDir, "log.jsonl"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644) //nolint:gosec
	if err != nil {
		log.Printf("executor: appending stray log entries: %v", err)
		return
	}
	_, werr := f.Write(entries)
	if cerr := f.Close(); werr != nil || cerr != nil {
		log.Printf("executor: appending stray log entries: %v %v", werr, cerr)
		return
	}
	_ = os.Remove(filepath.Join(stray, "log.jsonl"))
}
