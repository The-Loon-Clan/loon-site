package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/the-loon-clan/loon/blob"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Fetching an image a MEMBER named (pluginapi.ImageIntake).
//
// The cover cache next door does the same job without most of what follows, and
// the difference is where the URL came from. A cover URL comes from TMDB; a
// screenshot URL comes from whoever is posting, which makes this a request the
// SERVER makes, from inside the network, to an address an attacker chose. That
// is the whole of server-side request forgery, and an image field is its most
// common front door: a cloud metadata endpoint, a port scan of a private
// subnet, or — on the production host, which routes egress through a VPN — a
// way to make the site reveal its real address by asking it to fetch something.
//
// So: the address is checked before the connection, and again on every redirect
// and every address the resolver returns, because a hostname that resolved
// publicly a moment ago can resolve to 127.0.0.1 on the next lookup.

const (
	// intakeTimeout bounds the whole fetch. Short: a member is waiting on a
	// form submission, and a host that takes thirty seconds to answer is a
	// host whose image is not worth the wait.
	intakeTimeout = 10 * time.Second
	// maxIntakeBytes bounds one image. A 4K screenshot is 1–3 MB.
	maxIntakeBytes = 6 << 20
	// maxIntakeRedirects is how far a link may bounce. Some hosts redirect
	// twice; anything past that is a chain worth refusing.
	maxIntakeRedirects = 3
)

// imageIntake implements pluginapi.ImageIntake over the host's blob store.
type imageIntake struct {
	files blob.Store
	http  *http.Client
}

var _ pluginapi.ImageIntake = (*imageIntake)(nil)

// newImageIntake builds the client.
func newImageIntake(files blob.Store) *imageIntake {
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		// Control, NOT DialContext, and the difference is the whole guard.
		//
		// The transport hands DialContext the address as it appears in the URL
		// — a HOSTNAME — because resolving is the dialler's job. A check there
		// sees "images.example.com", cannot parse it as an IP, and either
		// refuses every named host (fail-closed and useless, which is how this
		// was first written and how the first real fetch caught it) or waves
		// everything through.
		//
		// Control runs AFTER resolution and BEFORE connect, once per address
		// the resolver returned, and is handed the actual ip:port about to be
		// dialled. So the address being refused is exactly the address being
		// connected to — which also closes DNS rebinding, where a name that
		// resolves publicly on a pre-flight check resolves to 127.0.0.1 on the
		// lookup that matters.
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return errors.New("that link could not be resolved")
			}
			ip := net.ParseIP(host)
			if ip == nil || !publicIP(ip) {
				return errors.New("that link points inside a private network")
			}
			return nil
		},
	}
	return &imageIntake{
		files: files,
		http: &http.Client{
			Timeout:   intakeTimeout,
			Transport: &http.Transport{DialContext: dialer.DialContext},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= maxIntakeRedirects {
					return errors.New("that link redirects too many times")
				}
				// The scheme and host are re-checked on every hop. A public
				// https URL redirecting to http://169.254.169.254 is the
				// oldest trick in this family — and while Control would catch
				// the address anyway, refusing here says the useful thing.
				return checkIntakeURL(req.URL)
			},
		},
	}
}

// publicIP reports whether an address is one this site may connect to.
//
// A DENY list of the ranges that mean "inside", rather than an allow list of
// the ones that mean "outside": the space is enormous and an allow list would
// refuse half the internet, but the private space is small, enumerable and
// exactly what an attacker is aiming at.
func publicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	// 169.254.169.254 is link-local and already refused above; these are the
	// ranges Go's helpers do not cover.
	for _, cidr := range []string{
		"100.64.0.0/10",  // carrier-grade NAT, and what Tailscale uses
		"192.0.0.0/24",   // IETF protocol assignments
		"192.0.2.0/24",   // TEST-NET-1
		"198.18.0.0/15",  // benchmarking
		"198.51.100.0/24",// TEST-NET-2
		"203.0.113.0/24", // TEST-NET-3
		"240.0.0.0/4",    // reserved
		"::/128", "64:ff9b::/96", "2001:db8::/32",
	} {
		if _, n, err := net.ParseCIDR(cidr); err == nil && n.Contains(ip) {
			return false
		}
	}
	return true
}

// checkIntakeURL refuses a URL before it is ever fetched.
func checkIntakeURL(u *url.URL) error {
	if u == nil {
		return errors.New("that is not a link")
	}
	// https ONLY. Not a purity argument: this site fetches the image and then
	// serves it under its own name, so an http link is one anybody on the path
	// can replace with a picture of their choosing, published here as a
	// member's screenshot.
	if u.Scheme != "https" {
		return errors.New("the link has to be https")
	}
	if u.Hostname() == "" {
		return errors.New("that link has no host in it")
	}
	// Credentials in a URL are never in a screenshot link and are frequently in
	// an attempt to confuse whoever reads it.
	if u.User != nil {
		return errors.New("the link cannot carry a username or password")
	}
	// A bare IP is refused outright. Every real image host has a name, and an
	// address typed directly is somebody aiming at something.
	if ip := net.ParseIP(u.Hostname()); ip != nil && !publicIP(ip) {
		return errors.New("that link points inside a private network")
	}
	return nil
}

// FetchImage retrieves, checks and stores one image.
func (in *imageIntake) FetchImage(ctx context.Context, dir, remote string) (pluginapi.StoredImage, error) {
	remote = strings.TrimSpace(remote)
	if len(remote) > 2000 {
		return pluginapi.StoredImage{}, errors.New("that link is too long")
	}
	u, err := url.Parse(remote)
	if err != nil {
		return pluginapi.StoredImage{}, errors.New("that is not a link")
	}
	if err := checkIntakeURL(u); err != nil {
		return pluginapi.StoredImage{}, err
	}
	if in.files == nil {
		return pluginapi.StoredImage{}, errors.New("this site has nowhere to store images")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return pluginapi.StoredImage{}, errors.New("that is not a link")
	}
	req.Header.Set("User-Agent", "loon-demo-site/1.0 (+screenshot intake)")
	req.Header.Set("Accept", "image/*")

	resp, err := in.http.Do(req)
	if err != nil {
		// The transport's own message is used when it is one of ours (the
		// address rules above), and a generic one otherwise — a dial error
		// carrying a hostname and a port is exactly the scan result this
		// refuses to hand back.
		return pluginapi.StoredImage{}, intakeError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return pluginapi.StoredImage{}, fmt.Errorf("that link answered %d", resp.StatusCode)
	}

	// +1 so a file exactly at the cap is detected as over it rather than
	// silently truncated into a corrupt image.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxIntakeBytes+1))
	if err != nil {
		return pluginapi.StoredImage{}, errors.New("that link stopped responding")
	}
	if len(data) > maxIntakeBytes {
		return pluginapi.StoredImage{}, fmt.Errorf("that image is over %d MB", maxIntakeBytes>>20)
	}
	// Sniff the CONTENT, never the URL's extension or the declared
	// Content-Type: this writes a file the site then serves under its own
	// domain, and a host answering an HTML page from a .png path would
	// otherwise be stored and served as an image.
	mime, ext, err := blob.SniffImage(data)
	if err != nil {
		return pluginapi.StoredImage{}, errors.New("that link is not an image this site can show")
	}

	// Named by the CONTENT hash, so the same picture posted twice is stored
	// once and a name can never collide or be chosen by whoever supplied it.
	sum := sha256.Sum256(data)
	name := path.Join(dir, hex.EncodeToString(sum[:])[:32]+ext)
	stored, err := in.files.Save(ctx, name, data)
	if err != nil {
		return pluginapi.StoredImage{}, errors.New("this site could not store that image")
	}
	return pluginapi.StoredImage{URL: stored, MIME: mime, Bytes: int64(len(data))}, nil
}

// intakeError turns a transport failure into something safe to show.
func intakeError(err error) error {
	// Our own refusals travel up wrapped in *url.Error; their text is written
	// to be shown and says nothing about what answered.
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		msg := ue.Err.Error()
		for _, ours := range []string{
			"private network", "redirects too many times", "has to be https",
			"no host in it", "username or password", "not a link", "could not be resolved",
		} {
			if strings.Contains(msg, ours) {
				return errors.New(msg)
			}
		}
	}
	// Everything else — refused connections, timeouts, TLS failures — collapses
	// to one sentence. The difference between "connection refused" and "timed
	// out" is precisely the signal a port scan is reading.
	return errors.New("that link could not be fetched")
}
