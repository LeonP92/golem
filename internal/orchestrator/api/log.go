package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/sse"
)

// RegisterLogRoutes adds log-ingestion and SSE streaming routes to mux.
func (h *Handlers) RegisterLogRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/tickets/{id}/log",
		auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.postLog)))
	mux.Handle("POST /api/tickets/{id}/log/document",
		auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.postLogDocument)))
	mux.Handle("GET /sse/tickets/{id}/log",
		auth.RequireSession(h.DB)(http.HandlerFunc(h.sseLog)))
}

// postLog inserts a LogEntry with a server-assigned sequence_num. Any
// authenticated shem may post to any ticket; ownership is not enforced here.
func (h *Handlers) postLog(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var body struct {
		EntryType string `json:"entry_type"`
		FromRole  string `json:"from_role"`
		ToRole    string `json:"to_role"`
		Message   string `json:"message"`
		Model     string `json:"model"`
		Backend   string `json:"backend"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Determine next sequence_num.
	var maxSeq struct{ Max *uint }
	h.DB.Model(&db.LogEntry{}).
		Select("MAX(sequence_num) as max").
		Where("ticket_id = ?", id).
		Scan(&maxSeq)
	var nextSeq uint = 1
	if maxSeq.Max != nil {
		nextSeq = *maxSeq.Max + 1
	}

	entry := db.LogEntry{
		TicketID:    id,
		SequenceNum: nextSeq,
		EntryType:   body.EntryType,
		FromRole:    body.FromRole,
		ToRole:      body.ToRole,
		Message:     body.Message,
		Model:       body.Model,
		Backend:     body.Backend,
		CreatedAt:   time.Now(),
	}
	if err := h.DB.Create(&entry).Error; err != nil {
		// Retry once on unique constraint (race with another appender).
		nextSeq++
		entry.SequenceNum = nextSeq
		if err2 := h.DB.Create(&entry).Error; err2 != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	// Create a human_input row when the entry is directed at a human.
	if body.ToRole == "human" {
		kind := "question_answer"
		if body.EntryType == "BLOCKER" {
			kind = "blocker_ack"
		}
		hi := db.HumanInput{
			TicketID:  id,
			Kind:      kind,
			Prompt:    body.Message,
			CreatedAt: time.Now(),
		}
		if err := h.DB.Create(&hi).Error; err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	// Publish to SSE broker.
	if h.Broker != nil {
		h.Broker.Publish(id, sse.LogEntryEvent{
			SequenceNum: entry.SequenceNum,
			EntryType:   entry.EntryType,
			FromRole:    entry.FromRole,
			ToRole:      entry.ToRole,
			Message:     entry.Message,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]uint{"sequence_num": entry.SequenceNum}) //nolint:errcheck
}

// postLogDocument accepts a streaming upload of a document (e.g. spec or plan).
// Metadata is passed via headers:
//
//	X-Entry-Type: SPEC | PLAN
//	X-From-Role:  developer (or other role)
//
// The raw request body is read as the document content — no JSON wrapping.
func (h *Handlers) postLogDocument(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	entryType := r.Header.Get("X-Entry-Type")
	fromRole := r.Header.Get("X-From-Role")
	if entryType == "" {
		http.Error(w, "missing X-Entry-Type header", http.StatusBadRequest)
		return
	}

	content, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(content) == 0 {
		http.Error(w, "empty document", http.StatusBadRequest)
		return
	}

	// SPEC and PLAN are singletons per ticket — upsert so re-submissions
	// replace the existing entry rather than accumulating duplicates.
	var entry db.LogEntry
	upsert := h.DB.
		Where("ticket_id = ? AND entry_type = ?", id, entryType).
		First(&entry)
	if upsert.Error == nil {
		// Row exists: update message and timestamp in place.
		if err := h.DB.Model(&entry).Updates(map[string]any{
			"message":    string(content),
			"from_role":  fromRole,
			"created_at": time.Now(),
		}).Error; err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	} else {
		// New row: assign the next sequence_num and insert.
		var maxSeq struct{ Max *uint }
		h.DB.Model(&db.LogEntry{}).
			Select("MAX(sequence_num) as max").
			Where("ticket_id = ?", id).
			Scan(&maxSeq)
		var nextSeq uint = 1
		if maxSeq.Max != nil {
			nextSeq = *maxSeq.Max + 1
		}
		entry = db.LogEntry{
			TicketID:    id,
			SequenceNum: nextSeq,
			EntryType:   entryType,
			FromRole:    fromRole,
			Message:     string(content),
			CreatedAt:   time.Now(),
		}
		if err := h.DB.Create(&entry).Error; err != nil {
			nextSeq++
			entry.SequenceNum = nextSeq
			if err2 := h.DB.Create(&entry).Error; err2 != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		}
	}

	if h.Broker != nil {
		h.Broker.Publish(id, sse.LogEntryEvent{
			SequenceNum: entry.SequenceNum,
			EntryType:   entry.EntryType,
			FromRole:    entry.FromRole,
			Message:     entry.Message,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]uint{"sequence_num": entry.SequenceNum}) //nolint:errcheck
}

// sseLog streams LogEntryEvents for a ticket as Server-Sent Events.
func (h *Handlers) sseLog(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ch, cancel := h.Broker.Subscribe(id)
	defer cancel()

	for {
		select {
		case evt, open := <-ch:
			if !open {
				return
			}
			var payload string
			if h.LogEntryHTML != nil {
				payload = h.LogEntryHTML(evt)
			} else {
				data, _ := json.Marshal(evt)
				payload = string(data)
			}
			fmt.Fprintf(w, "data: %s\n\n", payload) //nolint:errcheck
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
