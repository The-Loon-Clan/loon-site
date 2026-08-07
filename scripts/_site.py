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


def require_site():
    """Exit with a clear message when the site is not up, rather than reporting
    every page as broken."""
    code, _ = get("/healthz")
    if code != 200:
        raise SystemExit(
            "audit: %s is not answering (/healthz -> %s).\n"
            "Start it with ./deploy.sh --dev, or set AUDIT_BASE." % (BASE, code)
        )
