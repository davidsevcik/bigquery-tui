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
	// The connection goes back to the pool, so the DSN's wait is restored
	// whatever happens below, even if setting it to 0 failed half way (a
	// cancel); a connection that cannot be restored is dropped.
	defer func() {
		if _, rerr := conn.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeoutMs)); rerr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout = 0"); err != nil {
		return false, err
	}
	var busy, logPages, moved int
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logPages, &moved); err != nil {
		if isBusy(err) {
			return false, nil
		}
		return false, err
	}
	return busy == 0, nil
}

// walTrimmer runs at most one background retry loop per catalog. A request
// that arrives while the loop runs is kept (pending), so the loop does not
// end on a success that came before it.
type walTrimmer struct {
	mu      sync.Mutex
	running bool
	pending bool
	stopped bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// retry starts the loop with ctx's values but not its cancellation: the unit
// that asked has finished. The loop ends on success, after walRetryFor, or
// when stop cancels it, even while it waits for a pooled connection.
func (t *walTrimmer) retry(ctx context.Context, db *sql.DB) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}
	t.pending = true
	if t.running {
		return
	}
	t.running = true
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), walRetryFor)
	t.cancel = cancel
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		defer cancel()
		tick := time.NewTicker(walRetryEvery)
		defer tick.Stop()
		for {
			select {
			case <-wctx.Done():
				t.finish()
				return // the journal size limit cuts the WAL at a later write
			case <-tick.C:
			}
			t.mu.Lock()
			t.pending = false
			t.mu.Unlock()
			done, err := truncateWAL(wctx, db)
			if (done || err != nil) && t.finishUnlessPending() {
				return
			}
		}
	}()
}

// finishUnlessPending ends the loop unless a new request came in during the
// last attempt; it reports whether the loop ended.
func (t *walTrimmer) finishUnlessPending() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending {
		return false
	}
	t.running = false
	return true
}

func (t *walTrimmer) finish() {
	t.mu.Lock()
	t.running = false
	t.mu.Unlock()
}

// stop ends the retry loop, before the database closes under it.
func (t *walTrimmer) stop() {
	t.mu.Lock()
	t.stopped = true
	if t.cancel != nil {
		t.cancel()
	}
	t.mu.Unlock()
	t.wg.Wait()
}
