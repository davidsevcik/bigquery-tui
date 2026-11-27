package engine

import (
	"context"
	gosync "sync"
)

type background struct {
	mu      gosync.Mutex
	wg      gosync.WaitGroup
	cancels map[uint64]context.CancelFunc
	next    uint64
	stopped bool
}

func (b *background) start(parent context.Context) (ctx context.Context, release func(), err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return nil, nil, ErrClosed
	}
	if b.cancels == nil {
		b.cancels = map[uint64]context.CancelFunc{}
	}
	ctx, cancel := context.WithCancel(parent)
	b.next++
	id := b.next
	b.cancels[id] = cancel
	b.wg.Add(1)
	return ctx, func() {
		cancel()
		b.mu.Lock()
		delete(b.cancels, id)
		b.mu.Unlock()
		b.wg.Done()
	}, nil
}

func (b *background) closed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stopped
}

func (b *background) stop() {
	b.mu.Lock()
	b.stopped = true
	for _, cancel := range b.cancels {
		cancel()
	}
	b.mu.Unlock()
	b.wg.Wait()
}
