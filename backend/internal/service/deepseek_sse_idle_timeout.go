package service

import (
	"bufio"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

var errDeepSeekSSEDataIntervalTimeout = errors.New("DeepSeek SSE stream data interval timeout")

// scanDeepSeekSSELinesWithIdleTimeout keeps Scanner's parsing semantics while
// making a blocked upstream read interruptible by the gateway stream idle
// timeout. In asynchronous mode, timeout and early handler termination close
// the body and join the reader goroutine before returning the response/account
// slot; ordinary EOF and the synchronous path leave body ownership to callers.
func scanDeepSeekSSELinesWithIdleTimeout(
	scanner *bufio.Scanner,
	body io.Closer,
	idleTimeout time.Duration,
	handleLine func(string) bool,
) error {
	if idleTimeout <= 0 {
		for scanner.Scan() {
			if !handleLine(scanner.Text()) {
				return nil
			}
		}
		return scanner.Err()
	}

	type scanEvent struct {
		line string
		err  error
	}
	events := make(chan scanEvent, 16)
	done := make(chan struct{})
	readerDone := make(chan struct{})
	var stopOnce sync.Once
	stopReader := func(closeBody bool) {
		stopOnce.Do(func() {
			close(done)
			if closeBody && body != nil {
				_ = body.Close()
			}
		})
		<-readerDone
	}
	defer stopReader(false)
	sendEvent := func(event scanEvent) bool {
		select {
		case events <- event:
			return true
		case <-done:
			return false
		}
	}
	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	go func() {
		defer close(readerDone)
		defer close(events)
		for scanner.Scan() {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			if !sendEvent(scanEvent{line: scanner.Text()}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			_ = sendEvent(scanEvent{err: err})
		}
	}()

	idleTimer := time.NewTimer(idleTimeout)
	defer idleTimer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if event.err != nil {
				return event.err
			}
			if !handleLine(event.line) {
				stopReader(true)
				return nil
			}
		case <-idleTimer.C:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			idleFor := time.Since(lastRead)
			if idleFor < idleTimeout {
				idleTimer.Reset(idleTimeout - idleFor)
				continue
			}
			stopReader(true)
			return errDeepSeekSSEDataIntervalTimeout
		}
	}
}
