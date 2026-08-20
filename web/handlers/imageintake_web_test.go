package handlers

import (
	"net"
	"net/url"
	"strings"
	"testing"
)

// TestPrivateAddressesAreRefused is the SSRF rule. Every one of these is a real
// target: the cloud metadata endpoint, the loopback interface where the admin
// panel of everything on the box listens, and the private ranges an image field
// is otherwise a port scanner for.
func TestPrivateAddressesAreRefused(t *testing.T) {
	refuse := []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"10.0.0.5", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", // the one everybody means by "SSRF"
		"0.0.0.0", "255.255.255.255",
		"100.64.0.1",  // carrier-grade NAT / Tailscale
		"198.18.0.1",  // benchmarking
		"192.0.2.1",   // TEST-NET-1
		"240.0.0.1",   // reserved
		"fe80::1",     // link-local v6
		"fc00::1",     // unique local v6
		"2001:db8::1", // documentation v6
	}
	for _, s := range refuse {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("test bug: %q is not an IP", s)
		}
		if publicIP(ip) {
			t.Errorf("%s was allowed — an image field that reaches it is a port scanner", s)
		}
	}
}

func TestPublicAddressesAreAllowed(t *testing.T) {
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:4700::1111"} {
		ip := net.ParseIP(s)
		if !publicIP(ip) {
			t.Errorf("%s was refused — the deny list has caught the open internet", s)
		}
	}
}

func TestCheckIntakeURL(t *testing.T) {
	bad := map[string]string{
		"http://example.com/a.png":       "https",
		"ftp://example.com/a.png":        "https",
		"https://user:pw@example.com/a":  "username or password",
		"https://127.0.0.1/a.png":        "private network",
		"https://[::1]/a.png":            "private network",
		"https://169.254.169.254/latest": "private network",
	}
	for raw, want := range bad {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("test bug: %v", err)
		}
		err = checkIntakeURL(u)
		if err == nil {
			t.Errorf("%s was accepted", raw)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s refused with %q, want something about %q", raw, err, want)
		}
	}
	ok, _ := url.Parse("https://images.example.com/shot.png")
	if err := checkIntakeURL(ok); err != nil {
		t.Errorf("an ordinary https image link was refused: %v", err)
	}
}

// TestRefusalsDoNotLeakWhatAnswered. The difference between "connection
// refused" and "i/o timeout" is exactly the signal a port scan reads, so
// everything that is not one of our own rules collapses to one sentence.
func TestRefusalsDoNotLeakWhatAnswered(t *testing.T) {
	for _, raw := range []error{
		&url.Error{Op: "Get", URL: "https://x/", Err: net.UnknownNetworkError("connection refused")},
		&url.Error{Op: "Get", URL: "https://x/", Err: net.UnknownNetworkError("i/o timeout")},
		&url.Error{Op: "Get", URL: "https://x/", Err: net.UnknownNetworkError("no such host")},
	} {
		got := intakeError(raw).Error()
		if got != "that link could not be fetched" {
			t.Errorf("leaked %q — a scanner reads the difference between these", got)
		}
	}
}

// TestOurOwnRefusalsSurvive — the address rules are written to be shown, and
// collapsing them too would tell a member nothing about what they typed wrong.
func TestOurOwnRefusalsSurvive(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "https://x/",
		Err: net.UnknownNetworkError("that link points inside a private network")}
	if got := intakeError(err).Error(); !strings.Contains(got, "private network") {
		t.Errorf("got %q, want the rule's own sentence", got)
	}
}
