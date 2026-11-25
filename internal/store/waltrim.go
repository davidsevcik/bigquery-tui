package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync"
	"time"
)

var (
	walRetryEvery = 2 * time.Second
	walRetryFor   = 10 * time.Minute
)

var walCheckpoint = truncateWAL

func truncateWAL(ctx context.Context, db *sql.DB) (done bool, err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
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

type walTrimmer struct {
	mu      sync.Mutex
	running bool
	pending bool
	stopped bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

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
				return
			case <-tick.C:
			}
			t.mu.Lock()
			t.pending = false
			t.mu.Unlock()
			done, err := walCheckpoint(wctx, db)
			if (done || err != nil) && t.finishUnlessPending() {
				return
			}
		}
	}()
}

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

func (t *walTrimmer) stop() {
	t.mu.Lock()
	t.stopped = true
	if t.cancel != nil {
		t.cancel()
	}
	t.mu.Unlock()
	t.wg.Wait()
}
