package service

import (
	"bufio"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type deepSeekBlockingTailReadCloser struct {
	prefix      []byte
	sentPrefix  bool
	readBlocked chan struct{}
	closed      chan struct{}
	readerDone  chan struct{}
	blockedOnce sync.Once
	closeOnce   sync.Once
	doneOnce    sync.Once
}

func newDeepSeekBlockingTailReadCloser(prefix string) *deepSeekBlockingTailReadCloser {
	return &deepSeekBlockingTailReadCloser{
		prefix:      []byte(prefix),
		readBlocked: make(chan struct{}),
		closed:      make(chan struct{}),
		readerDone:  make(chan struct{}),
	}
}

func (r *deepSeekBlockingTailReadCloser) Read(p []byte) (int, error) {
	if !r.sentPrefix && len(r.prefix) > 0 {
		r.sentPrefix = true
		return copy(p, r.prefix), nil
	}
	r.blockedOnce.Do(func() { close(r.readBlocked) })
	<-r.closed
	r.doneOnce.Do(func() { close(r.readerDone) })
	return 0, io.ErrClosedPipe
}

func (r *deepSeekBlockingTailReadCloser) Close() error {
	r.closeOnce.Do(func() { close(r.closed) })
	return nil
}

func TestScanDeepSeekSSELinesWithIdleTimeoutStopsReaderGoroutine(t *testing.T) {
	tests := []struct {
		name        string
		prefix      string
		idleTimeout time.Duration
		handleLine  func(string) bool
		wantTimeout bool
	}{
		{
			name:        "timeout",
			idleTimeout: 20 * time.Millisecond,
			handleLine:  func(string) bool { return true },
			wantTimeout: true,
		},
		{
			name:        "handler stop",
			prefix:      "data: stop\n",
			idleTimeout: time.Second,
			handleLine:  func(string) bool { return false },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := newDeepSeekBlockingTailReadCloser(tt.prefix)
			scanner := bufio.NewScanner(body)

			err := scanDeepSeekSSELinesWithIdleTimeout(scanner, body, tt.idleTimeout, tt.handleLine)

			if tt.wantTimeout {
				require.ErrorIs(t, err, errDeepSeekSSEDataIntervalTimeout)
			} else {
				require.NoError(t, err)
			}
			requireClosedChannel(t, body.readBlocked)
			requireClosedChannel(t, body.closed)
			requireClosedChannel(t, body.readerDone)
		})
	}
}

func requireClosedChannel(t *testing.T, channel <-chan struct{}) {
	t.Helper()
	select {
	case <-channel:
	default:
		t.Fatal("expected channel to be closed")
	}
}
