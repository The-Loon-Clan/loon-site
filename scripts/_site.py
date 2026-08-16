"""Shared helpers for the audit scripts: log in once, fetch pages.

Standard library only, deliberately. These run on a developer's machine and in
deploy.sh, and a tool that needs `pip install` before it can tell you the site
is broken is a tool nobody runs.
"""
import http.cookiejar
import os
import re
import urllib.error
import urllib.parse
import urllib.request

BASE = os.environ.get("AUDIT_BASE", "http://localhost:8090")
USER = os.environ.get("AUDIT_USER", "alice")
PASS = os.environ.get("AUDIT_PASS", "alice")
# A NON-staff account, for audits that have to tell "needs a session" apart from
# "needs a role". Without one, every gated page looks the same from outside and
# an audit can only report that admin pages refuse strangers — which is the
# easy half.
MEMBER_USER = os.environ.get("AUDIT_MEMBER", "bob")
MEMBER_PASS = os.environ.get("AUDIT_MEMBER_PASS", "bob")

_jar = http.cookiejar.CookieJar()
_opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(_jar))
# Some pages branch on Accept: the auth middleware redirects a browser and
# returns 401 JSON to anything else, so an audit without this reports every
# gated page as an error.
_opener.addheaders = [("Accept", "text/html,application/xhtml+xml")]


def get(path):
    """Fetch a site path. Returns (status, body). Status 0 means the request
    itself failed -- a connection refused is a different problem from a 500 and
    the caller should be able to tell them apart."""
    url = path if path.startswith("http") else BASE + path
    try:
        r = _opener.open(url, timeout=20)
        return r.getcode(), r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")
    except Exception as e:  # noqa: BLE001 -- connection refused, DNS, timeout
        return 0, str(e)


def login():
    """Sign in as the audit user. Returns True on success.

    Signed IN on purpose: half the site is behind a session, and an audit that
    only ever sees the logged-out shell checks the smallest and least
    interesting part of it."""
    code, body = get("/login")
    if code != 200:
        return False
    m = re.search(r'name="_csrf" value="([^"]+)"', body)
    if not m:
        return False
    data = urllib.parse.urlencode(
        {"username": USER, "password": PASS, "_csrf": m.group(1)}
    ).encode()
    try:
        _opener.open(urllib.request.Request(BASE + "/login", data=data), timeout=20).read()
    except Exception:  # noqa: BLE001
        return False
    _, home = get("/")
    return USER in home


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    """Report a redirect as a redirect.

    urllib follows them by default, and for an access audit that is fatal: the
    auth middleware sends a BROWSER to /login rather than answering 401, so a
    following client reports the login page's 200 and every gated route looks
    open. The first run of audit_access.py said exactly that about eighteen
    admin pages, against a curl probe that had already shown 401 — which is the
    only reason it was caught.
    """

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Session:
    """One browser: its own cookie jar, its own signed-in identity.

    The module-level helpers below share a single session, which is right for an
    audit that just needs to be logged in. It is not enough for the access
    audit, which has to hold an anonymous, a member and an admin session at once
    and compare what each is served — with one jar they would overwrite each
    other's cookies and the comparison would silently be three probes of the
    same identity.
    """

    def __init__(self, follow_redirects=True):
        self.jar = http.cookiejar.CookieJar()
        handlers = [urllib.request.HTTPCookieProcessor(self.jar)]
        if not follow_redirects:
            handlers.append(_NoRedirect())
        self.opener = urllib.request.build_opener(*handlers)
        self.opener.addheaders = [("Accept", "text/html,application/xhtml+xml")]

    def get(self, path):
        url = path if path.startswith("http") else BASE + path
        try:
            r = self.opener.open(url, timeout=20)
            return r.getcode(), r.read().decode("utf-8", "replace")
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode("utf-8", "replace")
        except Exception as e:  # noqa: BLE001
            return 0, str(e)

    def status(self, path):
        return self.get(path)[0]

    def csrf(self, path="/"):
        """A token this session can actually use.

        Fetched per call rather than cached: a token is bound to the session
        that fetched it, and a stale one is refused before any gate is
        consulted — which is exactly how a probe comes back 403 and proves
        nothing."""
        _, body = self.get(path)
        m = re.search(r'name="_csrf" value="([^"]+)"', body)
        return m.group(1) if m else ""

    def login(self, user, password):
        token = self.csrf("/login")
        if not token:
            return False
        data = urllib.parse.urlencode(
            {"username": user, "password": password, "_csrf": token}).encode()
        try:
            self.opener.open(
                urllib.request.Request(BASE + "/login", data=data), timeout=20).read()
        except urllib.error.HTTPError as e:
            # A SUCCESSFUL login is a redirect. On a session that does not
            # follow them that arrives as an HTTPError, so treating every error
            # as failure makes it impossible to sign in at all — which is how
            # this returned False for a correct password.
            if e.code not in (301, 302, 303, 307, 308):
                return False
        except Exception:  # noqa: BLE001
            return False
        # The cookie is what matters, and the home page naming the user is the
        # only proof it took.
        return user in self.get("/")[1]

    def post(self, path, fields=None):
        """POST with a VALID token for this session. Returns the status."""
        payload = dict(fields or {})
        payload["_csrf"] = self.csrf("/")
        data = urllib.parse.urlencode(payload).encode()
        try:
            r = self.opener.open(
                urllib.request.Request(BASE + path, data=data), timeout=20)
            return r.getcode()
        except urllib.error.HTTPError as e:
            return e.code
        except Exception:  # noqa: BLE001
            return 0


def winpath(p):
    """MSYS path -> a path Windows Python and Windows Chrome can open.

    These scripts are run from a bash prompt whose paths look like
    /c/Program Files/..., and handed to programs that have never heard of them.
    Converting rather than requiring one spelling, because the alternative is a
    FileNotFoundError naming a file in a form that looks perfectly correct.

    Lives here rather than in mobile.py because shot.py needs the same fix, and
    a second copy is a second thing to get wrong on the next platform.
    """
    p = p.replace("\\", "/")
    m = re.match(r"^/([a-zA-Z])/(.*)$", p)
    return "%s:/%s" % (m.group(1), m.group(2)) if m else p


# The one machine-specific path, shared by every script that drives a browser.
# Overridable, so none of them is pinned to whoever wrote it.
CHROME = winpath(os.environ.get(
    "CHROME", "C:/Program Files/Google/Chrome/Application/chrome.exe"
))


def unmangle(arg):
    """Undo MSYS path conversion on a URL path argument.

    Git Bash rewrites any argument starting with "/" into a Windows path before
    the program sees it, so `python scripts/shot.py x /tracker` arrives as
    "C:/Program Files/Git/tracker" and the script reports the SITE as broken —
    an error that names an HTTP status and says nothing about the real cause.

    Defended here rather than documented, because the fix is invisible from the
    error and the error is what somebody will search for. MSYS_NO_PATHCONV=1
    also works, for anyone who already knows to reach for it.
    """
    m = re.match(r"^[A-Za-z]:[\\/].*?[\\/](?:Git|usr|mingw64)([\\/].*)$", arg)
    return m.group(1).replace("\\", "/") if m else arg


def require_site():
    """Exit with a clear message when the site is not up, rather than reporting
    every page as broken."""
    code, _ = get("/healthz")
    if code != 200:
        raise SystemExit(
            "audit: %s is not answering (/healthz -> %s).\n"
            "Start it with ./deploy.sh --dev, or set AUDIT_BASE." % (BASE, code)
        )
