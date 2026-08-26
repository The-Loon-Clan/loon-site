#!/usr/bin/env python3
"""Conformance tests against the REAL clients that consume this indexer.

    python3 scripts/clienttest.py              # every client
    python3 scripts/clienttest.py hydra        # just one
    python3 scripts/clienttest.py --keep       # leave containers up to poke at

WHY THIS EXISTS
---------------
Every other check in scripts/ reads this site's own output and decides whether
it looks right. That is the wrong judge for an API whose whole purpose is to be
consumed by somebody else's software: a Newznab feed can be well-formed, valid
against the spec, and still useless to the two programs anyone actually points
at an indexer.

The bug that prompted this proves it. `/api?t=tvsearch&tvdbid=121361` — a
request for ONE show — answered with the entire 160,673-release catalogue,
because the id was unsupported, ignored, and left nothing to filter on. It was
well-formed XML. It had the right content type. Every static check passed it.
NZBHydra2 found it in ten minutes, because Hydra probes with id parameters and
no query at all, which is a shape no hand-written test here would have thought
to send. Production had recorded the same failure from the same client
("NZBHydra caches those as matches") without either side connecting it.

So this runs the actual clients, in actual containers, against the running
demo, and asserts what they OBSERVE.

WHAT IT ASSERTS, AND WHY NOT FIXED NUMBERS
------------------------------------------
The index is a live crawl; counts move whenever the crawler runs. A test that
hardcoded "Breaking Bad returns 318" would fail for a reason that is not a bug,
which is the fastest way to get a suite ignored. So every assertion is either
an INVARIANT or a COMPARISON against what the demo itself reports for the same
query in the same run:

    the client accepts the indexer at all        (caps parses)
    a text search returns something              (> 0)
    the client agrees with the demo's own count  (the conformance property)
    narrowing narrows                            (season/ep < unnarrowed)
    an unanswerable id search returns nothing    (the regression above)
    the NZB link the client is handed downloads  (what a downloader needs)

Fixed numbers appear nowhere.
"""

import argparse
import json
import os
import re
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from http import cookiejar as http_cookiejar

# Everything addresses everything else by SERVICE NAME on the demo's network
# (see docker-compose.clients.yml). Nothing is published on the host, so there
# is no port to collide with a real Prowlarr, Hydra or SAB the operator runs on
# the same machine -- and no way for this suite to reconfigure one by accident.
#
# DEMO_URL is overridable only so the file can still be pointed at a site on a
# different network; the default is what the compose file supplies.
DEMO = os.environ.get("DEMO_URL", "http://app:8090")
DEMO_INTERNAL = DEMO
QUERY = "Breaking Bad"   # any show the seeded index carries
TV_SEASON, TV_EPISODE = 4, 1


# ── plumbing ────────────────────────────────────────────────────────────

def http(url, data=None, headers=None, method=None, timeout=60):
    req = urllib.request.Request(url, data=data, method=method)
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")
    except Exception as e:                                    # noqa: BLE001
        return 0, str(e)


def wait_for(fn, what, secs=180):
    """Poll until fn() is truthy. Clients take a while to boot; a fixed sleep
    is either flaky or slow, and this suite is slow enough already."""
    deadline = time.time() + secs
    while time.time() < deadline:
        try:
            if fn():
                return True
        except Exception:                                     # noqa: BLE001
            pass
        time.sleep(2)
    print("    timed out waiting for %s after %ds" % (what, secs))
    return False


def demo_api_key():
    """A member's Newznab key, read the way a member gets it: sign in, open the
    API-key page, take the value out of the field it is displayed in.

    Over HTTP rather than out of the database, so the suite needs no Postgres
    client and no Docker socket -- which is what lets the whole thing run as an
    ordinary container.

    Anchored on the field's class rather than "the first 32 hex characters on
    the page": the page also carries a CSRF token, and a loose pattern picks
    that instead. The request then fails as "Incorrect user credentials", which
    reads like a bug in the API and is really a bug in the test.
    """
    sess = Session()
    if not sess.login("bob", "bob"):
        return ""
    _, body = sess.get("/p/api-key")
    m = re.search(r'font-monospace" value="([0-9a-f]{32,})"', body)
    return m.group(1) if m else ""


def demo_total(key, params):
    """What the demo ITSELF reports for a query — the yardstick the clients are
    measured against, fetched in the same run so a moving index cannot make the
    comparison wrong."""
    url = DEMO + "/api?apikey=" + key + "&" + params
    _, body = http(url)
    m = body.split('total="')
    return int(m[1].split('"')[0]) if len(m) > 1 else -1


class Session:
    """Just enough of a browser to sign in and read a page.

    Not scripts/_site.py: that helper is written for a person running against
    localhost, and this runs inside a container against a service name.
    """

    def __init__(self):
        self.jar = http_cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(self.jar))

    def get(self, path):
        try:
            with self.opener.open(DEMO + path, timeout=30) as r:
                return r.getcode(), r.read().decode("utf-8", "replace")
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode("utf-8", "replace")
        except Exception as e:                                # noqa: BLE001
            return 0, str(e)

    def login(self, user, pw):
        _, page = self.get("/login")
        m = re.search(r'name="_csrf" value="([^"]+)"', page)
        if not m:
            return False
        data = urllib.parse.urlencode(
            {"username": user, "password": pw, "_csrf": m.group(1)}).encode()
        try:
            self.opener.open(urllib.request.Request(DEMO + "/login", data=data),
                             timeout=30).read()
        except urllib.error.HTTPError as e:
            # A successful login is a redirect, which surfaces as an error on
            # an opener that follows them into a page it cannot read.
            if e.code not in (301, 302, 303, 307, 308):
                return False
        except Exception:                                     # noqa: BLE001
            return False
        return user in self.get("/")[1]


class Report:
    def __init__(self):
        self.rows = []

    def check(self, client, name, ok, detail=""):
        self.rows.append((client, name, bool(ok), detail))
        print("    %-4s %-42s %s" % ("PASS" if ok else "FAIL", name, detail))
        return ok

    def failed(self):
        return [r for r in self.rows if not r[2]]

    def summary(self):
        bad = self.failed()
        print("\n" + "=" * 72)
        print("clients: %d checks, %d failed" % (len(self.rows), len(bad)))
        for c, n, _, d in bad:
            print("  FAIL  %-8s %s  %s" % (c, n, d))
        if not bad:
            print("Every client agrees with the demo about what this index holds.")
        return 1 if bad else 0


# ── a client under test ─────────────────────────────────────────────────

class Client:
    """One real consumer, started fresh and thrown away.

    Fresh every run on purpose: a client that has already learned this
    indexer's capabilities would skip the very negotiation being tested, and a
    reused config is how a suite starts passing for reasons nobody can name.
    """
    name = ""
    image = ""
    port = 0            # published on the host, deliberately not the default
    internal_port = 0   # what the client listens on inside its container
    container = ""

    # No start/stop: docker-compose.clients.yml owns the lifecycle, so this
    # file never shells out to Docker and needs no socket. A client is already
    # booting by the time the runner starts; ready() is what waits for it.

    def base(self):
        return "http://%s:%d" % (self.name, self.internal_port)


class Hydra(Client):
    """NZBHydra2 — a Newznab AGGREGATOR, and the stricter of the two.

    It does not take an indexer's word for anything: caps advertising
    season/ep means nothing to Hydra until its own probe has confirmed it
    (allCapsChecked), and it decides what an indexer supports by running real
    searches and checking how many results actually match. That is what makes
    it worth the container.
    """
    name = "hydra"
    image = "ghcr.io/linuxserver/nzbhydra2:latest"
    port = 15076
    internal_port = 5076
    container = "loon-clienttest-hydra"

    def ready(self):
        code, _ = http(self.base() + "/internalapi/config", timeout=5)
        return code == 200

    def configure(self, key):
        code, body = http(self.base() + "/internalapi/config")
        if code != 200:
            return None
        cfg = json.loads(body)
        cfg["indexers"] = [{
            "name": "loon", "host": DEMO_INTERNAL, "apiKey": key,
            "searchModuleType": "NEWZNAB", "state": "ENABLED", "enabled": True,
            "score": 0, "showOnSearch": True, "preselect": True,
            "categoryMapping": {}, "enabledCategories": [],
            "supportedSearchIds": [], "supportedSearchTypes": [],
            "backend": "NEWZNAB", "configComplete": True, "allCapsChecked": False,
            "hitLimit": None, "downloadLimit": None, "timeout": None,
            "userAgent": None, "username": None, "password": None,
            "hitLimitResetTime": None, "loadLimitOnRandom": None,
            "vipExpirationDate": None,
        }]
        # PUT, not POST: Hydra's config endpoint refuses POST outright, which
        # reads as "wrong payload" and is really "wrong verb".
        code, _ = http(self.base() + "/internalapi/config",
                       data=json.dumps(cfg).encode(),
                       headers={"Content-Type": "application/json"}, method="PUT")
        if code != 200:
            return None
        return cfg.get("main", {}).get("apiKey")

    def caps_check(self):
        """Hydra's own capability negotiation, which is the interesting half."""
        code, body = http(self.base() + "/internalapi/indexer/checkCaps",
                          data=json.dumps({"indexerName": "loon"}).encode(),
                          headers={"Content-Type": "application/json"},
                          method="POST", timeout=180)
        return code == 200

    def search_total(self, own_key, params):
        code, body = http(self.base() + "/api?apikey=" + own_key + "&" + params,
                          timeout=180)
        if code != 200 or 'total="' not in body:
            return -1
        return int(body.split('total="')[1].split('"')[0])

    def run(self, rep, key):
        own = self.configure(key)
        rep.check(self.name, "accepts the indexer config", bool(own))
        if not own:
            return
        rep.check(self.name, "completes its own caps negotiation", self.caps_check())

        q = urllib.parse.urlencode({"t": "search", "q": QUERY})
        mine = self.search_total(own, q)
        theirs = demo_total(key, q)
        rep.check(self.name, "text search returns results", mine > 0,
                  "%d results" % mine)
        rep.check(self.name, "agrees with the demo's own count", mine == theirs,
                  "client %d vs demo %d" % (mine, theirs))

        # The regression this suite was written for.
        idq = urllib.parse.urlencode({"t": "tvsearch", "tvdbid": "121361"})
        rep.check(self.name, "unanswerable id search stays empty",
                  demo_total(key, idq) == 0,
                  "an id we cannot resolve must not answer with the catalogue")


class Prowlarr(Client):
    """Prowlarr — what most people actually put in front of an indexer now, and
    the thing Sonarr and Radarr talk to rather than talking to us directly.

    Its API needs the key it generates on first boot, which lives in a config
    file inside the container rather than anywhere it will tell you over HTTP.
    """
    name = "prowlarr"
    image = "lscr.io/linuxserver/prowlarr:latest"
    port = 19696
    internal_port = 9696
    container = "loon-clienttest-prowlarr"
    key = None

    def ready(self):
        self.key = self.key or self._api_key()
        if not self.key:
            return False
        code, _ = http(self.base() + "/api/v1/health",
                       headers={"X-Api-Key": self.key}, timeout=5)
        return code == 200

    def _api_key(self):
        """Prowlarr generates its key into config.xml on first boot and will
        not hand it out over HTTP without it -- a chicken and egg the shared
        volume solves. Mounted READ-ONLY, so the suite can learn the key and
        cannot rewrite the client's configuration."""
        try:
            with open("/clientconfig/prowlarr/config.xml", encoding="utf-8") as fh:
                m = re.search(r"<ApiKey>([a-f0-9]+)</ApiKey>", fh.read())
                return m.group(1) if m else None
        except OSError:
            return None

    def _hdr(self):
        return {"X-Api-Key": self.key, "Content-Type": "application/json"}

    def configure(self, key):
        """Build the indexer from Prowlarr's OWN schema rather than a
        hand-written body: the schema names every field and its default, so a
        change on their side surfaces as a missing field here instead of a
        silently ignored one."""
        # Remove a "loon" left by an earlier run first. The client's config is
        # on a volume that outlives the container, so a second run otherwise
        # fails with "Name: Should be unique" -- a real collision reported as
        # if our indexer were malformed.
        code, body = http(self.base() + "/api/v1/indexer", headers=self._hdr())
        if code == 200:
            for existing in json.loads(body):
                if existing.get("name") == "loon":
                    http(self.base() + "/api/v1/indexer/%s" % existing["id"],
                         headers=self._hdr(), method="DELETE")

        code, body = http(self.base() + "/api/v1/indexer/schema", headers=self._hdr())
        if code != 200:
            return False
        schema = next((s for s in json.loads(body)
                       if s.get("implementation") == "Newznab"), None)
        if not schema:
            return False
        idx = dict(schema)
        idx.update({"name": "loon", "enable": True,
                    "appProfileId": 1, "priority": 25,
                    "protocol": "usenet", "configContract": schema.get("configContract")})
        for f in idx.get("fields", []):
            if f.get("name") == "baseUrl":
                f["value"] = DEMO_INTERNAL
            elif f.get("name") == "apiKey":
                f["value"] = key
            elif f.get("name") == "apiPath":
                f["value"] = "/api"
        code, resp = http(self.base() + "/api/v1/indexer",
                          data=json.dumps(idx).encode(),
                          headers=self._hdr(), method="POST", timeout=120)
        if code not in (200, 201):
            self.last_error = resp[:200]
            return False
        self.indexer_id = json.loads(resp).get("id")
        return True

    def test_indexer(self):
        """Prowlarr's own 'Test' button — the check a person clicks before
        trusting an indexer, and the one that fails loudest when caps are
        wrong."""
        # The WHOLE saved indexer, not {"id": n}. Prowlarr validates the body
        # it is given rather than looking the id up, so a bare id fails as
        # "'Name' must not be empty" — which reads like the indexer is
        # misconfigured when it is really the request that was.
        code, body = http(self.base() + "/api/v1/indexer/%s" % self.indexer_id,
                          headers=self._hdr())
        if code != 200:
            return False, "could not read back the saved indexer: HTTP %d" % code
        code, body = http(self.base() + "/api/v1/indexer/test", data=body.encode(),
                          headers=self._hdr(), method="POST", timeout=180)
        return code in (200, 201, 202), body[:200]

    def search_count(self, q):
        code, body = http(self.base() + "/api/v1/search?" +
                          urllib.parse.urlencode({"query": q, "indexerIds": self.indexer_id}),
                          headers=self._hdr(), timeout=180)
        if code != 200:
            return -1
        try:
            return len(json.loads(body))
        except ValueError:
            return -1

    def run(self, rep, key):
        ok = self.configure(key)
        rep.check(self.name, "accepts the indexer config", ok,
                  getattr(self, "last_error", ""))
        if not ok:
            return
        passed, detail = self.test_indexer()
        rep.check(self.name, "passes its own indexer Test", passed, detail if not passed else "")

        n = self.search_count(QUERY)
        rep.check(self.name, "text search returns results", n > 0, "%d results" % n)
        # Prowlarr pages its own results, so this is a floor rather than an
        # equality: what matters is that it got real rows, not that it chose
        # the same page size we did.
        rep.check(self.name, "returns a plausible page of results", n >= 10,
                  "%d rows" % n)


class Sab(Client):
    """SABnzbd -- the downloader, and the only client here that is not a
    Newznab reader at all.

    That is exactly why it is worth running: Hydra and Prowlarr both judge us
    by our FEED, and a feed can be perfect while the thing it points at is
    broken. SAB judges us by whether the NZB actually downloads, parses, and
    queues -- and by whether the post-processing script this site hands its
    members reports back correctly afterwards.

    Its own "test" is `mode=version`, which is the connection check its API
    offers; SAB's Test buttons are for news SERVERS, not for indexers, so
    version-then-addurl is the closest thing to "test connection to us".
    """
    name = "sab"
    image = "lscr.io/linuxserver/sabnzbd:latest"
    internal_port = 8080
    container = "loon-clienttest-sab"
    key = None

    def base(self):
        """BY IP, not by service name, and this is not a style choice.

        SAB refuses any request whose Host header is a hostname it has not been
        told about (https://sabnzbd.org/hostname-check), and at first boot it
        whitelists only its own container id -- so `http://sab:8080` answers
        "Access denied - Hostname verification failed" with HTTP 200 and a
        sentence of prose. It looks nothing like an auth failure, which sends
        you hunting for a wrong API key.

        Whitelisting the service name in compose does not fix it: the option is
        read when the config is FIRST written, and that config lives on a
        volume that outlives the container. An IP Host header is allowed
        unconditionally, needs no SAB configuration, and cannot rot.
        """
        return "http://%s:%d" % (socket.gethostbyname(self.name), self.internal_port)

    def _api_key(self):
        """SAB writes its key into sabnzbd.ini on first boot. Same shared
        read-only volume as Prowlarr, same reason."""
        try:
            with open("/clientconfig/sab/sabnzbd.ini", encoding="utf-8") as fh:
                m = re.search(r"^api_key\s*=\s*(\S+)", fh.read(), re.M)
                return m.group(1) if m else None
        except OSError:
            return None

    def ready(self):
        self.key = self.key or self._api_key()
        if not self.key:
            return False
        code, body = self.api("mode=version", timeout=5)
        return code == 200 and body.strip() != ""

    def api(self, params, timeout=60):
        return http("%s/api?apikey=%s&output=json&%s"
                    % (self.base(), self.key, params), timeout=timeout)

    def nzb_link(self, key):
        """A real download link out of a real feed -- the same one a member
        would click, rather than a URL this test assembled and therefore
        cannot be wrong about."""
        _, feed = http(DEMO + "/api?apikey=" + key + "&"
                       + urllib.parse.urlencode({"t": "search", "q": QUERY}))
        for chunk in feed.split("<link>")[1:]:
            cand = chunk.split("</link>")[0].replace("&amp;", "&")
            if "t=get" in cand:
                # The feed is built with the site's public base URL, which is
                # localhost from the host's point of view and unreachable from
                # inside this network. The PATH is what is being tested.
                return DEMO + cand[cand.find("/api"):]
        return ""

    def run(self, rep, key):
        rep.check(self.name, "answers its own version check", bool(self.key))

        link = self.nzb_link(key)
        if not rep.check(self.name, "a feed result carries a download link", bool(link)):
            return

        # PUSH: hand SAB the URL and let IT fetch, parse and queue the NZB.
        # This is the half a Newznab client cannot test -- the feed can be
        # perfect while the file behind it is not an NZB at all.
        code, body = self.api("mode=addurl&name=" + urllib.parse.quote(link, safe=""),
                              timeout=180)
        # Parsed, not pattern-matched: SAB answers {"status":true,"nzo_ids":[…]}
        # with no space after the colon, and a string test for '"status": true'
        # calls a success a failure while printing the proof that it worked.
        ok = False
        try:
            ok = code == 200 and json.loads(body).get("status") is True
        except ValueError:
            pass
        rep.check(self.name, "accepts our NZB by URL (addurl)", ok, "" if ok else body[:120])

        # And it has to actually ARRIVE. addurl returning true only means SAB
        # took the job; the fetch happens afterwards, so a URL that 404s still
        # answers true here and fails silently a second later.
        def queued():
            _, q = self.api("mode=queue")
            _, h = self.api("mode=history")
            return QUERY.split()[0].lower() in (q + h).lower()

        rep.check(self.name, "the NZB reaches its queue or history",
                  wait_for(queued, "SAB to fetch the NZB", 90),
                  "addurl can succeed and the fetch still fail")

    def run_report_script(self, rep, key):
        """The RETURN half: the post-processing script this site serves its
        members, run the way SAB runs it, against the live report endpoint.

        Fetched from the site rather than kept in this repo, so what is tested
        is the file members are actually given -- with its site URL and API key
        already substituted in.
        """
        sess = Session()
        if not sess.login("bob", "bob"):
            return rep.check(self.name, "can fetch the report script", False, "login failed")
        code, script = sess.get("/p/downloads/script?key=" + key)
        if not rep.check(self.name, "the site serves a configured report script",
                         code == 200 and "__API_KEY__" not in script,
                         "HTTP %d" % code):
            return

        path = "/tmp/report.py"
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(script)

        # GRAB FIRST, then report on what was grabbed.
        #
        # The endpoint resolves a job to a release by folding the job NAME and
        # matching it against that member's recent grabs, so reporting a job
        # nobody downloaded can only ever answer "could not match" -- which the
        # script exits 0 on, because an unmatched report is not the member's
        # fault. Asserting on that proves the endpoint is reachable and nothing
        # more. Downloading a real NZB first is what makes the match possible,
        # and the match is the feature.
        title = self.grab_a_release(key)
        if not rep.check(self.name, "can grab a release to report on", bool(title)):
            return
        # SAB passes positional arguments; argv[3] is the job name and argv[7]
        # the exit status, 0 meaning the download completed.
        r = subprocess.run([sys.executable, path, "/tmp", "", title, "", "", "", "", "0"],
                           capture_output=True, text=True, timeout=120)
        out = (r.stdout + r.stderr).strip()
        last = out.splitlines()[-1][:140] if out else ""
        rep.check(self.name, "the report script runs", r.returncode == 0, last)
        # And that the site MATCHED it, rather than politely declining to.
        rep.check(self.name, "the site matches the report to the grab",
                  "could not match" not in out.lower(), last)

    def grab_a_release(self, key):
        """Download one NZB as the member, which is what records the grab the
        report is later matched against. Returns the release title."""
        _, feed = http(DEMO + "/api?apikey=" + key + "&"
                       + urllib.parse.urlencode({"t": "search", "q": QUERY}))
        title = ""
        if "<title>" in feed:
            for chunk in feed.split("<title>")[1:]:
                cand = chunk.split("</title>")[0]
                if QUERY.split()[0].lower() in cand.lower():
                    title = cand
                    break
        link = self.nzb_link(key)
        if not title or not link:
            return ""
        code, _ = http(link)
        return title if code == 200 else ""


# ── checks that need no client at all ───────────────────────────────────

def api_contract_checks(rep, key):
    """The properties every consumer depends on, asserted directly.

    Here as well as through the clients because a client failure says "this
    broke" and these say WHICH thing broke — and because they run in a second,
    so a change can be checked without waiting for two containers to boot.
    """
    q = urllib.parse.urlencode({"t": "search", "q": QUERY})
    total = demo_total(key, q)
    rep.check("api", "text search finds the seeded show", total > 0, "%d" % total)

    # Spaces and dots must reach the same releases. Scene names use dots,
    # people and *arr clients type spaces, and for a long time neither query
    # ever showed what the other could see.
    dotted = demo_total(key, urllib.parse.urlencode(
        {"t": "search", "q": QUERY.replace(" ", ".")}))
    rep.check("api", "spaces and dots reach the same index", total == dotted,
              "spaces %d vs dots %d" % (total, dotted))

    # Narrowing must narrow. tvsearch ignoring season/ep is invisible from the
    # outside: the response is a valid feed either way.
    narrowed = demo_total(key, urllib.parse.urlencode(
        {"t": "tvsearch", "q": QUERY, "season": TV_SEASON, "ep": TV_EPISODE}))
    unnarrowed = demo_total(key, urllib.parse.urlencode({"t": "tvsearch", "q": QUERY}))
    rep.check("api", "season and ep actually narrow", 0 < narrowed < unnarrowed,
              "%d of %d" % (narrowed, unnarrowed))

    # The regression that prompted this suite.
    for param in ("tvdbid", "imdbid", "tmdbid", "tvmazeid", "traktid", "rid"):
        n = demo_total(key, urllib.parse.urlencode({"t": "tvsearch", param: "121361"}))
        if not rep.check("api", "%s= answers empty, not everything" % param, n == 0,
                         "returned %d" % n):
            break

    # caps has to declare what the plugin can now do, or a client that trusts
    # caps never sends the parameters.
    _, caps = http(DEMO + "/api?t=caps&apikey=" + key)
    rep.check("api", "caps declares season and ep",
              "season" in caps and "ep" in caps and "tv-search" in caps)

    # What a DOWNLOADER needs: the link in a result has to actually yield an
    # NZB. This is the part SABnzbd would exercise, asserted without it.
    _, feed = http(DEMO + "/api?apikey=" + key + "&" + q)
    link = ""
    if "<link>" in feed:
        for chunk in feed.split("<link>")[1:]:
            cand = chunk.split("</link>")[0]
            if "/api" in cand and "t=get" in cand:
                link = cand.replace("&amp;", "&")
                break
    if not link:
        rep.check("api", "a result carries a downloadable NZB link", False,
                  "no t=get link in the feed")
        return
    code, body = http(link)
    looks_like_nzb = body.lstrip().startswith("<?xml") and "nzb" in body[:600].lower()
    rep.check("api", "that link downloads an NZB", code == 200 and looks_like_nzb,
              "HTTP %d, %d bytes" % (code, len(body)))


CLIENTS = {"hydra": Hydra, "prowlarr": Prowlarr, "sab": Sab}


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    # No argparse `choices=`: with nargs="*" it validates the DEFAULT too, so
    # an empty default (meaning "all") is rejected before main ever runs.
    ap.add_argument("clients", nargs="*",
                    help="which clients to run: %s (default: all)" % ", ".join(CLIENTS))
    ap.add_argument("--keep", action="store_true",
                    help="leave the containers running afterwards")
    args = ap.parse_args()
    unknown = [c for c in args.clients if c not in CLIENTS]
    if unknown:
        print("unknown client(s): %s — known: %s"
              % (", ".join(unknown), ", ".join(CLIENTS)))
        return 2

    code, _ = http(DEMO + "/healthz", timeout=10)
    if code == 0:
        print("the demo is not answering on %s - start it with "
              "`docker compose up -d` first" % DEMO)
        return 2

    key = demo_api_key()
    if not key:
        print("could not read a member API key from the database")
        return 2

    rep = Report()
    print("\napi - the contract itself")
    api_contract_checks(rep, key)

    for name in (args.clients or list(CLIENTS)):
        cls = CLIENTS[name]
        c = cls()
        print("\n%s - %s" % (name, cls.image))
        try:
            if not wait_for(c.ready, "%s to come up" % name):
                rep.check(name, "starts and answers its own API", False)
                continue
            rep.check(name, "starts and answers its own API", True)
            c.run(rep, key)
            if hasattr(c, "run_report_script"):
                c.run_report_script(rep, key)
        finally:
            # Always, even on an exception: this box is deliberately kept
            # quiet, and a suite that leaks containers is one nobody runs twice.
            pass  # compose owns teardown: `docker compose ... down -v`

    return rep.summary()


if __name__ == "__main__":
    sys.exit(main())
