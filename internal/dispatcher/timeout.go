package dispatcher

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

// errUpstreamTimeout is the cancellation cause when a plugin has not started
// answering within its request timeout. ProxyFor's error handler turns it into
// a 504; any other proxy error stays a 502.
var errUpstreamTimeout = errors.New("plugin did not start answering within its request timeout")

// headerDeadline bounds how long a plugin may take to start answering — to
// send its response headers — not how long the whole exchange may take.
//
// A deadline on the whole request would cut every long download, every
// server-sent event stream and every slow upload at the same 30 seconds, and
// the manifest says nothing about which routes stream. Once headers arrive the
// plugin is demonstrably working and the clock stops; a plugin that hangs
// before answering — the case a timeout is for — gets a 504 instead of holding
// a bulkhead slot forever.
//
// The clock starts when the request body has been sent, not when the request
// arrives: a large upload over a slow line is the client's time, not the
// plugin's.
type headerDeadline struct {
	timeout time.Duration
	cancel  context.CancelCauseFunc
	once    sync.Once
	mu      sync.Mutex
	timer   *time.Timer
	stopped bool
}

// withHeaderDeadline returns req under a context the deadline can cancel, and
// the deadline. A timeout of zero or less means none.
func withHeaderDeadline(req *http.Request, timeout time.Duration) (*http.Request, *headerDeadline) {
	ctx, cancel := context.WithCancelCause(req.Context())
	hd := &headerDeadline{timeout: timeout, cancel: cancel}
	req = req.WithContext(ctx)
	if timeout <= 0 {
		return req, hd
	}
	if req.Body == nil || req.Body == http.NoBody || req.ContentLength == 0 {
		hd.start()
	} else {
		req.Body = &bodyWatcher{ReadCloser: req.Body, onEOF: hd.start}
	}
	return req, hd
}

func (hd *headerDeadline) start() {
	hd.once.Do(func() {
		hd.mu.Lock()
		defer hd.mu.Unlock()
		if hd.stopped {
			return
		}
		hd.timer = time.AfterFunc(hd.timeout, func() { hd.cancel(errUpstreamTimeout) })
	})
}

// stop is called when headers arrive, and when the exchange ends.
func (hd *headerDeadline) stop() {
	hd.mu.Lock()
	defer hd.mu.Unlock()
	hd.stopped = true
	if hd.timer != nil {
		hd.timer.Stop()
	}
}

// done releases the context once the exchange is over.
func (hd *headerDeadline) done() {
	hd.stop()
	hd.cancel(nil)
}

// bodyWatcher calls onEOF once the request body has been read to the end.
type bodyWatcher struct {
	io.ReadCloser
	onEOF func()
}

func (b *bodyWatcher) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if errors.Is(err, io.EOF) {
		b.onEOF()
	}
	return n, err
}
