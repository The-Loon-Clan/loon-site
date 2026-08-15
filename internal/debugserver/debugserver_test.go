package debugserver

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The gate, and the four ways it is supposed to refuse.
//
// Worth testing properly rather than eyeballing, because every failure mode
// here is silent from the outside: a gate that lets everything through looks
// identical to one that works, right up until somebody finds the port.

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func request(t *testing.T, remote, auth, query string) *httptest.ResponseRecorder {
	t.Helper()
	served := false
	h := Gate("s3cret", quietLog(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	}))

	url := "/debug/pprof/"
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.RemoteAddr = remote
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if served && rec.Code != http.StatusOK {
		t.Fatal("handler ran but the response was not 200 — test is confused")
	}
	return rec
}

func TestTheRightTokenFromAnAllowedAddressPasses(t *testing.T) {
	if got := request(t, "127.0.0.1:5555", "Bearer s3cret", "").Code; got != http.StatusOK {
		t.Errorf("status = %d, want 200 — the gate refuses a valid request", got)
	}
}

func TestTheTokenMayAlsoComeFromTheQuery(t *testing.T) {
	// `go tool pprof` cannot set a header on every sub-request it makes, so
	// ?token= has to work or the tool is unusable against this endpoint.
	if got := request(t, "127.0.0.1:5555", "", "token=s3cret").Code; got != http.StatusOK {
		t.Errorf("status = %d, want 200 — go tool pprof would not work", got)
	}
}

func TestAPublicAddressIsRefused(t *testing.T) {
	if got := request(t, "203.0.113.7:5555", "Bearer s3cret", "").Code; got != http.StatusNotFound {
		t.Errorf("status = %d, want 404 — a routable address reached pprof", got)
	}
}

func TestAWrongTokenIsRefused(t *testing.T) {
	if got := request(t, "127.0.0.1:5555", "Bearer wrong", "").Code; got != http.StatusNotFound {
		t.Errorf("status = %d, want 404", got)
	}
}

func TestNoTokenIsRefused(t *testing.T) {
	if got := request(t, "127.0.0.1:5555", "", "").Code; got != http.StatusNotFound {
		t.Errorf("status = %d, want 404", got)
	}
}

// A refusal must be a 404 and not a 403.
//
// 403 confirms there is something here worth attacking; 404 says nothing. The
// distinction only matters against somebody scanning, which is exactly who
// this is for.
func TestARefusalRevealsNothing(t *testing.T) {
	rec := request(t, "203.0.113.7:5555", "", "")
	if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
		t.Errorf("status = %d — this confirms the endpoint exists", rec.Code)
	}
	if body := rec.Body.String(); len(body) > 40 {
		t.Errorf("refusal body is %d bytes; it should say nothing:\n%s", len(body), body)
	}
}

// The docker-proxy case, which is why the RFC1918 ranges are in the allowlist
// at all. With the userland proxy on, a request that genuinely arrived over a
// private interface is presented with the bridge gateway's address.
func TestABridgeGatewayAddressIsAllowed(t *testing.T) {
	if got := request(t, "172.18.0.1:5555", "Bearer s3cret", "").Code; got != http.StatusOK {
		t.Errorf("status = %d, want 200 — every Docker-proxied request would be "+
			"rejected and the endpoint would look broken rather than protected", got)
	}
}

// No token configured means the listener never starts. Fail-closed: forgetting
// to set it cannot leave pprof open, which is the failure mode that matters.
func TestAnUnsetTokenDoesNotStart(t *testing.T) {
	if Start(Config{Addr: "127.0.0.1:0", Token: "", Log: quietLog()}) {
		t.Error("Start reported it was listening with no token configured")
	}
}

// And the gate itself refuses everything if it is ever constructed with an
// empty token, independently of Start's guard.
func TestAnEmptyTokenRefusesEverything(t *testing.T) {
	h := Gate("", quietLog(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the handler ran with no token configured")
	}))
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// Nothing in this repository may serve http.DefaultServeMux.
//
// This test started life asserting that pprof was NOT on the default mux, and
// failed immediately: net/http/pprof registers itself there in init(), so
// importing it at all publishes /debug/pprof/ globally. A private mux does not
// prevent that.
//
// So the invariant that can actually be held is the other one — the global
// registration is harmless as long as nothing serves that mux. That is a
// property of this source tree, so this reads the source. If somebody ever
// writes http.ListenAndServe(addr, nil) or hands DefaultServeMux to a server,
// pprof becomes public on that port with no token, and this is what says so.
func TestNothingServesTheDefaultMux(t *testing.T) {
	var offenders []string
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Line comments are stripped first. The first version of this scan
		// flagged debugserver.go for the comment EXPLAINING the hazard, which
		// is a test failing on its own documentation — and the kind of false
		// positive that gets a useful check deleted rather than fixed.
		for i, line := range strings.Split(string(src), "\n") {
			if c := strings.Index(line, "//"); c >= 0 {
				line = line[:c]
			}
			for _, bad := range []string{
				"http.DefaultServeMux",
				"http.ListenAndServe(", // second arg nil serves the default mux
			} {
				if strings.Contains(line, bad) {
					offenders = append(offenders,
						fmt.Sprintf("%s:%d uses %s", path, i+1, bad))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range offenders {
		t.Errorf("%s — net/http/pprof registers on the default mux in init(), "+
			"so serving it publishes /debug/pprof/ with no token and no "+
			"allowlist on that listener", o)
	}
}

func TestTheMuxServesTheProfiles(t *testing.T) {
	m := Mux()
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/cmdline"} {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, rec.Code)
		}
	}
}

// A short token must not start the listener.
//
// The failure this prevents is an operator setting LOON_PPROF_TOKEN=hunter2,
// seeing pprof come up, and believing it is protected. A guessable token in
// front of heap dumps and a CPU denial of service is worse than no profiling,
// so this refuses rather than warns — a warning at boot is read once and then
// lives in a log nobody greps.
func TestAShortTokenDoesNotStart(t *testing.T) {
	for _, tok := range []string{
		"hunter2",
		"loon-indexer",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcde", // 63: one short
	} {
		if Start(Config{Addr: "127.0.0.1:0", Token: tok, Log: quietLog()}) {
			t.Errorf("Start listened with a %d-character token", len(tok))
		}
	}
}

// And the length that .env.example tells people to generate must pass, or the
// documentation and the code disagree — which is the failure the whole
// error-handling triage kept finding.
func TestTheDocumentedKeyLengthIsAccepted(t *testing.T) {
	// `openssl rand -hex 64` = 512 bits as 128 hex characters.
	tok := strings.Repeat("a1b2c3d4", 16)
	if len(tok) != 128 {
		t.Fatalf("test fixture is %d characters, not the 128 that -hex 64 produces", len(tok))
	}
	if len(tok) < MinTokenLen {
		t.Fatalf("the documented key length (%d) is below MinTokenLen (%d) — "+
			".env.example tells people to generate a key this code refuses", len(tok), MinTokenLen)
	}
}
