package worker

import (
	"fmt"
	"log"
	"strconv"

	"github.com/leonp92/golem/internal/models"
	"github.com/leonp92/golem/internal/shem/client"
)

// postLog sends one log entry to the orchestrator and prints its message
// locally. Errors posting to the orchestrator are logged and ignored —
// best-effort.
func postLog(c *client.Client, ticketID string, p client.LogPayload) {
	log.Printf("executor [%s]: %s", ticketID[:8], p.Message)
	if p.FromRole == "" {
		p.FromRole = "shem"
	}
	if _, err := c.PostLog(ticketID, p); err != nil {
		log.Printf("executor [%s]: postLog error: %v", ticketID[:8], err)
	}
}

func postStatus(c *client.Client, ticketID, msg string) {
	postLog(c, ticketID, client.LogPayload{EntryType: "STATUS", Message: msg})
}

func postWarning(c *client.Client, ticketID, msg string) {
	postLog(c, ticketID, client.LogPayload{EntryType: "WARNING", Message: msg})
}

// postPhaseStart announces a phase and the backend and model it runs on.
func postPhaseStart(c *client.Client, ticketID, backend, model, phase string) {
	shown := model
	if shown == "" {
		shown = "vendor default"
	}
	postLog(c, ticketID, client.LogPayload{
		EntryType: "STATUS",
		Message:   fmt.Sprintf("Starting agent (%s, model=%s) — %s phase", backend, shown, phase),
		Backend:   backend,
		Model:     model,
	})
}

// sanitizeModel returns model, or "" with a WARNING if it isn't a safe
// argv token. The shem is downstream of an orchestrator that may be newer
// than it, so an unusable value is an event to report rather than a reason
// to strand the ticket.
func sanitizeModel(c *client.Client, ticketID, model string) string {
	if model == "" || models.ValidModelID(model) {
		return model
	}
	postWarning(c, ticketID, "rejected model value "+strconv.Quote(model)+"; using the vendor default")
	return ""
}
