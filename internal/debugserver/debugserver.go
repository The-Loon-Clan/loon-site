// Package debugserver exposes net/http/pprof on a listener of its own, behind
// an IP allowlist and a token.
//
// Adapted from the prod indexer's cmd/main.go, which had already worked out
// the parts that are easy to get wrong. What is kept and why:
//
//   - A SEPARATE listener, not a route on the site. pprof on the main mux is
//     one middleware-ordering mistake away from being public, and it is the
//     single most useful thing to hand an attacker: heap dumps contain live
//     data, and /debug/pprof/profile is a CPU denial of service anyone can
//     start.
//
//   - OFF unless a token is set. An unset token does not mean "no auth", it
//     means the listener never starts. Fail-closed, so forgetting to configure
//     it cannot leave it open — which is the opposite of what a default-on
//     debug endpoint does.
//
//   - 404, never 403. A 403 confirms the endpoint is there and worth
//     attacking; a 404 says nothing. Same reasoning the site applies to
//     private profiles.
//
//   - Every rejection logged. A port scan or a token guess is then visible in
//     the log rather than being silently absorbed.
//
// The IP allowlist is defence in depth rather than the defence. The real
// protection is binding the port to an interface only the operator can reach
// (a Tailscale address, or 127.0.0.1); the allowlist catches a bind that was
// misconfigured, and the token catches anything past both.
package debugserver

import (
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"
	"time"
)

// allowedNets are the ranges a request may arrive from.
//
// The RFC1918 ranges are not optional paranoia — Docker's userland proxy
// rewrites the source address to the bridge gateway, so a request that really
// did come over a private interface arrives looking like 172.18.0.1. Without
// them every legitimate request is rejected and the endpoint appears broken
// rather than protected.
//
// 100.64.0.0/10 is the CGNAT range Tailscale uses.
var allowedCIDRs = []string{
	"127.0.0.0/8",
	"::1/128",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"100.64.0.0/10",
}

// Config is what the host supplies.
type Config struct {
	// Addr is the listen address, e.g. "127.0.0.1:6060". Bind it to an
	// interface only the operator can reach; the checks here are the second
	// and third layers, not the first.
	Addr string

	// Token must be presented as `Authorization: Bearer <token>` or ?token=.
	// EMPTY MEANS DISABLED — Start returns without listening.
	Token string

	Log *slog.Logger
}

// Start runs the pprof listener in the background, or does nothing when no
// token is configured.
//
// Returns whether it started, so the caller can say so at boot. A debug
// listener that is running should be visible in the log; one that is not
// should be too, or an operator cannot tell which they have.
func Start(cfg Config) bool {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Token == "" {
		cfg.Log.Info("pprof disabled", "reason", "no token configured")
		return false
	}
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:6060"
	}

	srv := &http.Server{
		Addr:    cfg.Addr,
		Handler: Gate(cfg.Token, cfg.Log, Mux()),
		// A profile capture is a long request by design (?seconds=30), so the
		// read timeout has to clear it. Everything else here is instant.
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      2 * time.Minute,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			cfg.Log.Error("pprof listener", "addr", cfg.Addr, "err", err)
		}
	}()
	cfg.Log.Info("pprof listening", "addr", cfg.Addr)
	return true
}

// Mux is the pprof handler set, registered explicitly.
//
// A correction worth recording, because the first version of this comment was
// confidently wrong and a test caught it. net/http/pprof registers itself on
// http.DefaultServeMux in its own init(), so merely IMPORTING it publishes
// /debug/pprof/ on the global mux. Building a private mux does not prevent
// that and cannot; nothing can, short of not using the package.
//
// What the private mux does buy is a written list of what is exposed, and a
// handler that can be gated. The global registration is inert here only
// because nothing in this process serves DefaultServeMux — which is an
// invariant rather than an accident, and debugserver_test.go asserts it
// against the source rather than hoping.
func Mux() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("/debug/pprof/", pprof.Index)
	m.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	m.HandleFunc("/debug/pprof/profile", pprof.Profile)
	m.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	m.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return m
}

// Gate wraps a handler with the IP allowlist and the token check.
//
// Exported so it can be tested without starting a listener, which is the whole
// reason this is a package rather than twenty lines in main.
func Gate(token string, log *slog.Logger, next http.Handler) http.Handler {
	var nets []*net.IPNet
	for _, c := range allowedCIDRs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			nets = append(nets, n)
		}
	}
	want := []byte(token)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(host)

		allowed := false
		for _, n := range nets {
			if ip != nil && n.Contains(ip) {
				allowed = true
				break
			}
		}
		if !allowed {
			log.Warn("pprof rejected", "reason", "address not allowed", "addr", r.RemoteAddr)
			http.NotFound(w, r)
			return
		}

		got := ""
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimPrefix(h, "Bearer ")
		} else {
			got = r.URL.Query().Get("token")
		}
		// ConstantTimeCompare, and the length check it needs: it returns 0 for
		// differing lengths without comparing, so a bare call still leaks
		// length through timing on the surrounding code. An empty configured
		// token cannot reach here — Start refuses to listen — but the check
		// stands on its own in case Gate is ever used elsewhere.
		if len(want) == 0 || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			log.Warn("pprof rejected", "reason", "bad or missing token", "addr", r.RemoteAddr)
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
