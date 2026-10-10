package dispatcher

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/config"
	"github.com/msrsiddik/apicorex/internal/manifest"
	"github.com/msrsiddik/apicorex/internal/protection"
	"github.com/msrsiddik/apicorex/internal/registry"
)

// These run requests through Dispatch to a real backend, because the bug
// they pin was in the wiring: per-plugin limits were read from the config
// and then not handed to the bulkhead, the breaker or the proxy.

type stack struct {
	engine *gin.Engine
	d      *Dispatcher
}

// newStack registers one plugin, "slow", whose config overrides every limit,
// in front of backend. The global defaults are deliberately loose, so a test
// that passes is passing on the plugin's own limit.
func newStack(t *testing.T, backend http.Handler, limits config.Limits) *stack {
	t.Helper()
	gin.SetMode(gin.TestMode)
	srv := httptest.NewServer(backend)
	t.Cleanup(srv.Close)

	cfg := config.Defaults()
	cfg.Plugins = map[string]config.Limits{"slow": limits}
	reg := registry.New()
	d := New(reg, protection.NewCircuitBreaker(100, time.Hour), protection.NewBulkhead(100), cfg)
	routes := []manifest.Route{{Method: "GET", Path: "/slow/*", Public: true}, {Method: "POST", Path: "/slow/*", Public: true}}
	if err := reg.Register("slow-1", "slow", srv.URL, "1", "internal", manifest.Manifest{Name: "slow", Routes: routes}, d.ProxyFor); err != nil {
		t.Fatal(err)
	}
	d.AddRoutes("slow-1", "slow", "internal", routes)
	e := gin.New()
	e.NoRoute(d.Dispatch)
	return &stack{engine: e, d: d}
}

func (s *stack) get(path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, live(httptest.NewRequest(http.MethodGet, path, nil)))
	return w
}

// live gives a test request the cancellable context a real server's request
// has. httptest.NewRequest's cannot be cancelled, and ReverseProxy then falls
// back to CloseNotify, which a ResponseRecorder does not implement.
func live(r *http.Request) *http.Request {
	ctx, cancel := context.WithCancel(r.Context())
	_ = cancel // released when the test ends; nothing waits on it
	return r.WithContext(ctx)
}

func TestPerPluginBulkheadLimit(t *testing.T) {
	release := make(chan struct{})
	var entered sync.WaitGroup
	entered.Add(1)
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow/hold" {
			entered.Done()
			<-release
		}
		w.WriteHeader(http.StatusOK)
	})
	s := newStack(t, backend, config.Limits{BulkheadMax: 1})

	done := make(chan int, 1)
	go func() { done <- s.get("/slow/hold").Code }()
	entered.Wait()
	if code := s.get("/slow/other").Code; code != http.StatusServiceUnavailable {
		t.Fatalf("second request with bulkhead_max 1: %d, want 503", code)
	}
	if got := s.d.ProtectionStatus("slow-1").BulkheadMax; got != 1 {
		t.Errorf("status reports bulkhead max %d", got)
	}
	close(release)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("held request: %d", code)
	}
}

func TestPerPluginCircuitBreakerThreshold(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	s := newStack(t, backend, config.Limits{CBThreshold: 2, CBResetTimeout: time.Hour})
	s.get("/slow/a")
	s.get("/slow/a")
	// The global threshold is 100; the plugin's own is 2.
	if code := s.get("/slow/a").Code; code != http.StatusServiceUnavailable {
		t.Fatalf("after 2 failures with cb_threshold 2: %d, want the breaker's 503", code)
	}
	if st := s.d.ProtectionStatus("slow-1").CircuitState; st != "open" {
		t.Errorf("breaker %s", st)
	}
}

func TestPerPluginCircuitBreakerReset(t *testing.T) {
	fail := true
	var mu sync.Mutex
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	// The global reset is an hour; the plugin's own is 50ms.
	s := newStack(t, backend, config.Limits{CBThreshold: 1, CBResetTimeout: 50 * time.Millisecond})
	s.get("/slow/a")
	if code := s.get("/slow/a").Code; code != http.StatusServiceUnavailable {
		t.Fatalf("after 1 failure with cb_threshold 1: %d, want the breaker open", code)
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	time.Sleep(80 * time.Millisecond)
	if code := s.get("/slow/a").Code; code != http.StatusOK {
		t.Fatalf("after the plugin's reset timeout: %d, want the probe through", code)
	}
}

func TestRequestTimeoutBeforeHeaders(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	})
	s := newStack(t, backend, config.Limits{RequestTimeout: 100 * time.Millisecond})
	start := time.Now()
	w := s.get("/slow/hang")
	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("hanging plugin: %d %s, want 504", w.Code, w.Body.String())
	}
	if el := time.Since(start); el > time.Second {
		t.Errorf("took %s; the timeout did not cut it", el)
	}
}

func TestStreamingOutlivesTheTimeout(t *testing.T) {
	// Headers at once, then a body that takes three times the timeout: a
	// download, an event stream. Cutting it would be the bug a whole-request
	// timeout has.
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for i := 0; i < 3; i++ {
			time.Sleep(100 * time.Millisecond)
			io.WriteString(w, "chunk;")
			w.(http.Flusher).Flush()
		}
	})
	s := newStack(t, backend, config.Limits{RequestTimeout: 100 * time.Millisecond})
	w := s.get("/slow/stream")
	if w.Code != http.StatusOK || w.Body.String() != "chunk;chunk;chunk;" {
		t.Fatalf("stream cut: %d %q", w.Code, w.Body.String())
	}
}

func TestSlowUploadIsTheClientsTime(t *testing.T) {
	// The body arrives slowly; the plugin answers as soon as it has it. The
	// clock must start when the body has been sent, not when the request came.
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write(b)
	})
	s := newStack(t, backend, config.Limits{RequestTimeout: 100 * time.Millisecond})
	pr, pw := io.Pipe()
	go func() {
		for i := 0; i < 3; i++ {
			time.Sleep(80 * time.Millisecond)
			pw.Write([]byte("part;"))
		}
		pw.Close()
	}()
	req := live(httptest.NewRequest(http.MethodPost, "/slow/upload", pr))
	req.ContentLength = -1
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "part;part;part;") {
		t.Fatalf("slow upload: %d %q", w.Code, w.Body.String())
	}
}

func TestPluginWithoutOverrideKeepsDefaults(t *testing.T) {
	cfg := config.Defaults()
	d := New(registry.New(), protection.NewCircuitBreaker(5, time.Second), protection.NewBulkhead(7), cfg)
	d.AddRoutes("p-1", "plain", "internal", []manifest.Route{{Method: "GET", Path: "/p"}})
	if got := d.ProtectionStatus("p-1").BulkheadMax; got != cfg.Default.BulkheadMax {
		t.Errorf("bulkhead %d, want the config default %d", got, cfg.Default.BulkheadMax)
	}
	d.RemoveRoutes("p-1")
	if got := d.ProtectionStatus("p-1").BulkheadMax; got != 7 {
		t.Errorf("after removal %d, want the bulkhead's own default", got)
	}
}
