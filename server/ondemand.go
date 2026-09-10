package server

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/msrsiddik/apicorex/internal/auth"
)

// On-demand TLS: the question a reverse proxy asks before issuing a certificate
// for a hostname nobody configured in advance.
//
// A school points its own domain at the platform, and there is no certificate
// for it because the platform did not know the name until that moment. Caddy
// will get one during the handshake — but only if something says the hostname
// is a customer's. Without that gate, anyone who points DNS here makes the
// platform request a certificate for their hostname, and the CA's rate limits
// turn that into an outage for every legitimate domain that needed one that
// week.
//
// Identity already knows the answer: ResolveDomain returns not-found for
// anything unverified or unknown. This is the smallest thing that turns that
// into the 200-or-not the proxy wants.

// OnDemandTLSPath is where the ask endpoint lives on the internal listener.
const OnDemandTLSPath = "/domain-allowed"

// NewOnDemandTLS builds the internal ask server, or nil when addr is empty.
//
// A listener of its own, rather than a route on the gateway, because the
// gateway's listener is the one the reverse proxy forwards the whole internet
// to — a route there would be reachable at
// "any-customer-domain.example/_core/domain-allowed", and a peer-address check
// could not tell that request apart from the proxy's own ask call, since both
// arrive from localhost.
//
// So it binds where the proxy can reach it and nothing else can. Bind it to a
// non-loopback address and that property is gone; the default keeps it.
func NewOnDemandTLS(resolver *auth.DomainResolver, addr string) *http.Server {
	if addr == "" {
		return nil
	}
	if resolver == nil {
		// No PLUGIN_API_KEY means no way to ask Identity anything. Refusing to
		// start beats a listener that answers "no" to every hostname and looks
		// like a DNS or CA problem.
		log.Printf("[ondemand] %s configured but PLUGIN_API_KEY is not set — not starting", addr)
		return nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc(OnDemandTLSPath, askHandler(resolver))
	return &http.Server{
		Addr:    addr,
		Handler: mux,
		// This sits inside a TLS handshake the visitor is waiting on. The
		// resolver has its own 5s timeout and a cache in front of Identity;
		// these bound the rest.
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
}

// askHandler answers "may this hostname have a certificate".
//
// 200 for a hostname some tenant has claimed and verified, and any other status
// for everything else. Deliberately says nothing in the body: the proxy reads
// the status, and a description of why a hostname was refused is a description
// of the customer list.
func askHandler(resolver *auth.DomainResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain")))
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		switch _, err := resolver.Resolve(ctx, host); {
		case err == nil:
			w.WriteHeader(http.StatusOK)
		case errors.Is(err, auth.ErrUnavailable):
			// Identity is down. Refusing means no *new* certificates until it
			// is back; certificates already issued keep working, because the
			// proxy only asks about names it has none for. Answering yes here
			// would hand the rate-limit problem to whoever asked next.
			log.Printf("[ondemand] cannot reach identity, refusing %q", host)
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// RunOnDemandTLS serves until ctx is done. A no-op for a nil server, so the
// caller need not branch.
func RunOnDemandTLS(ctx context.Context, srv *http.Server) error {
	if srv == nil {
		return nil
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	log.Printf("[ondemand] on-demand TLS ask endpoint on %s%s", srv.Addr, OnDemandTLSPath)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
