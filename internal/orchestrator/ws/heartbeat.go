package ws

import (
	"context"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

// StartHeartbeatMonitor runs a goroutine that checks for shems that have not
// sent a heartbeat within timeout. Dead shems are marked offline and their
// in-progress tickets are returned to the unassigned pool.
// The goroutine stops when ctx is cancelled.
func StartHeartbeatMonitor(ctx context.Context, gdb *gorm.DB, hub *Hub, checkInterval, timeout time.Duration) {
	go func() {
		t := time.NewTicker(checkInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				cutoff := time.Now().Add(-timeout)
				var dead []db.Shem
				gdb.Where("status = 'online' AND last_heartbeat < ?", cutoff).Find(&dead)
				for _, s := range dead {
					sid := s.ID
					gdb.Model(&db.Shem{}).Where("id = ?", sid).Updates(map[string]any{
						"status": "offline", "current_ticket": nil,
					})
					gdb.Model(&db.Ticket{}).
						Where("assigned_shem = ? AND phase IN ('claimed','brainstorm','plan','implement','review')", sid).
						Updates(map[string]any{"phase": "unassigned", "assigned_shem": nil})
					hub.Unregister(sid)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}
