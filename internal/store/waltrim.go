package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync"
	"time"
)

// walRetryEvery and walRetryFor pace the background retries of a WAL
// truncation that a reader blocked.
var (
	walRetryEvery = 2 * time.Second
	walRetryFor   = 10 * time.Minute
)

// truncateWAL runs one wal_checkpoint(TRUNCATE) without waiting on locks:
// done is false when a reader or a writer was in the way (SQLite then
// reports busy=1, or SQLITE_BUSY for the write lock).
func truncateWAL(ctx context.Context, db *sql.DB) (done bool, err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout = 0"); err != nil {
		return false, err
	}
	// the connection goes back to the pool: restore the DSN's wait
	defer func() {
		if _, rerr := conn.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeoutMs)); rerr != nil {
			// ErrBadConn makes database/sql drop the connection instead of pooling it
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	var busy, logPages, done2 int
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logPages, &done2); err != nil {
		if isBusy(err) {
			return false, nil
		}
		return false, err
	}
	return busy == 0, nil
}

// walTrimmer runs at most one background retry loop per catalog.
type walTrimmer struct {
	mu      sync.Mutex
	running bool
	quit    chan struct{}
	wg      sync.WaitGroup
}

// retry starts the loop with ctx's values but not its cancellation: the unit
// that asked has finished, and only stop ends the loop.
func (t *walTrimmer) retry(ctx context.Context, db *sql.DB) {
	ctx = context.WithoutCancel(ctx)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running {
		return
	}
	if t.quit == nil {
		t.quit = make(chan struct{})
	}
	t.running = true
	quit := t.quit
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		defer func() { t.mu.Lock(); t.running = false; t.mu.Unlock() }()
		tick := time.NewTicker(walRetryEvery)
		defer tick.Stop()
		deadline := time.After(walRetryFor)
		for {
			select {
			case <-quit:
				return
			case <-deadline:
				return // the journal size limit cuts the WAL at a later write
			case <-tick.C:
				if done, err := truncateWAL(ctx, db); done || err != nil {
					return
				}
			}
		}
	}()
}

// stop ends the retry loop, before the database closes under it.
func (t *walTrimmer) stop() {
	t.mu.Lock()
	if t.quit != nil {
		select {
		case <-t.quit:
		default:
			close(t.quit)
		}
	}
	t.mu.Unlock()
	t.wg.Wait()
}
