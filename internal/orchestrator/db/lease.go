package db

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Lease is a named, expiring lock with a fencing token. One holder acts for
// the named resource at a time — a scheduler per run, the outbox drainer —
// and every action it takes carries the token it was given, so a stale
// holder that loses the lease (or never noticed it expired) cannot write
// after a newer holder started: its token no longer matches.
//
// The token is monotonic per lease name and is handed out only by
// AcquireLease, which is also the only place the holder changes. TTL-based
// expiry replaces the holder automatically after a crash; the fencing
// token, not the expiry, is what makes acting-after-expiry safe.
type Lease struct {
	Name         string    `gorm:"primaryKey" json:"name"`
	HolderID     string    `gorm:"not null" json:"holder_id"`
	FencingToken int64     `gorm:"not null" json:"fencing_token"`
	ExpiresAt    time.Time `gorm:"not null" json:"expires_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// AcquireLease takes or extends the named lease for holder and returns the
// new fencing token. It succeeds when the lease is free, expired, or already
// held by this holder (a re-acquire still bumps the token: a resumed
// scheduler must not reuse a token a crashed incarnation of itself might
// still be acting on). It reports acquired=false when another holder's
// lease is still valid.
func AcquireLease(db *gorm.DB, name, holder string, ttl time.Duration) (token int64, acquired bool, err error) {
	// Single atomic statements, no read-then-write transaction: on SQLite a
	// deferred transaction that upgrades to a write fails at once with
	// SQLITE_BUSY when another goroutine wrote in between.
	now := time.Now()
	ins := db.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&Lease{Name: name, HolderID: holder, FencingToken: 1, ExpiresAt: now.Add(ttl)})
	if ins.Error != nil {
		return 0, false, fmt.Errorf("create lease %s: %w", name, ins.Error)
	}
	if ins.RowsAffected == 1 {
		return 1, true, nil
	}
	upd := db.Model(&Lease{}).
		Where("name = ? AND (holder_id = ? OR expires_at < ?)", name, holder, now).
		Updates(map[string]any{
			"holder_id": holder, "fencing_token": gorm.Expr("fencing_token + 1"), "expires_at": now.Add(ttl),
		})
	if upd.Error != nil {
		return 0, false, fmt.Errorf("update lease %s: %w", name, upd.Error)
	}
	if upd.RowsAffected == 0 {
		return 0, false, nil // another holder's lease is still valid
	}
	var l Lease
	if err := db.Where("name = ? AND holder_id = ?", name, holder).First(&l).Error; err != nil {
		return 0, false, fmt.Errorf("read lease %s: %w", name, err)
	}
	return l.FencingToken, true, nil
}

// RenewLease extends the lease only while holder still owns it at the given
// fencing token. false means the lease was lost — the caller must stop
// acting for the resource and re-acquire before doing anything more.
func RenewLease(db *gorm.DB, name, holder string, token int64, ttl time.Duration) (bool, error) {
	res := db.Model(&Lease{}).
		Where("name = ? AND holder_id = ? AND fencing_token = ?", name, holder, token).
		Updates(map[string]any{"expires_at": time.Now().Add(ttl)})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// CheckFencing reports whether token is the lease's current fencing token.
// A writer that cannot prove it still holds the lease must not write; this
// is the check that stops a stale scheduler or drainer from acting after a
// newer one started.
func CheckFencing(db *gorm.DB, name string, token int64) (bool, error) {
	var l Lease
	if err := db.Where("name = ?", name).First(&l).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return l.FencingToken == token, nil
}

// ReleaseLease gives up the lease, but only if holder still owns it at the
// given token — releasing someone else's lease would let a third party
// acquire while the real holder still believes it owns the resource. The
// row is expired, not deleted, so the next token still climbs.
func ReleaseLease(db *gorm.DB, name, holder string, token int64) error {
	res := db.Model(&Lease{}).
		Where("name = ? AND holder_id = ? AND fencing_token = ?", name, holder, token).
		Update("expires_at", time.Now().Add(-time.Second))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("lease %s is not held by %s at token %d", name, holder, token)
	}
	return nil
}
