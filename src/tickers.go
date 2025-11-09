package downloader

import (
	"sync"
	"time"
)

type TickerFanOut struct {
	subscribers []*subscriber
	mu          sync.Mutex
}

type subscriber struct {
	remove bool
	ch     chan struct{}
}

// Fans out ticker to all clients and removes clients that are marked remove.
func NewTickerFanOut(interval time.Duration) *TickerFanOut {
	f := &TickerFanOut{}
	ticker := time.NewTicker(interval)

	go func() {
		for range ticker.C {
			f.mu.Lock()
			for i := len(f.subscribers) - 1; i >= 0; i-- {
				sub := f.subscribers[i]
				if sub.remove {
					close(sub.ch)
					f.subscribers = append(f.subscribers[:i], f.subscribers[i+1:]...)
					continue
				}

				select {
				case sub.ch <- struct{}{}:
				default:
				}
			}
			f.mu.Unlock()
		}
	}()

	return f
}

// Provides a channel that broadcasts the ticker and a close function for the channel.
func (f *TickerFanOut) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1) // buffered
	f.mu.Lock()
	sub := subscriber{ch: ch}
	f.subscribers = append(f.subscribers, &sub)
	f.mu.Unlock()
	unsubscribe := func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, sub := range f.subscribers {
			if sub.ch == ch {
				sub.remove = true
				break
			}
		}

	}
	return ch, unsubscribe
}
