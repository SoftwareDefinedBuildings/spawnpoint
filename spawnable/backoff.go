package spawnable

import (
	"sync"
	"time"
)

type SmartPoller struct {
	min       time.Duration
	max       time.Duration
	interval  time.Duration
	adjust    time.Duration
	lastReset time.Time
	cb        func()
	preempt   chan struct{}
	mu        sync.Mutex
}

// A smart poller will call cb every interval, and double the interval every
// adjust, up to maxInterval. If reset(), the interval goes down to minInterval
func NewSmartPoller(minInterval, maxInterval, adjust time.Duration, cb func()) *SmartPoller {
	rv := SmartPoller{
		min:       minInterval,
		max:       maxInterval,
		adjust:    adjust,
		interval:  minInterval,
		lastReset: time.Now(),
	}
	go func() {
		for {
			select {
			case <-rv.preempt:
			case <-time.After(rv.interval):
			}
			cb()
			rv.mu.Lock()
			if time.Now().Sub(rv.lastReset) > rv.adjust {
				rv.interval *= 2
				if rv.interval > rv.max {
					rv.interval = rv.max
				}
				rv.lastReset = time.Now()
			}
			rv.mu.Unlock()
		}
	}()
	return &rv
}

func (sp *SmartPoller) Reset() {
	sp.mu.Lock()
	sp.lastReset = time.Now()
	sp.interval = sp.min
	sp.mu.Unlock()
	sp.preempt <- struct{}{}
}
