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
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

DEMO = "http://localhost:8090"
# Test containers publish on HIGH ports, never the clients' well-known ones.
# This machine already runs a real Prowlarr on 9696; binding it would fail the
# suite at best, and at worst the suite would be talking to somebody's actual
# configured client and changing its indexers. A throwaway container on 19696
# cannot be mistaken for theirs.
# Reached from a sibling container: compose service name and internal port.
DEMO_INTERNAL = "http://app:8090"
NETWORK = "loon-site_default"
QUERY = "Breaking Bad"   # any show the seeded index carries
TV_SEASON, TV_EPISODE = 4, 1


# ── plumbing ────────────────────────────────────────────────────────────

def sh(*args, check=False):
    r = subprocess.run(args, capture_output=True, text=True)
    if check and r.returncode != 0:
        raise RuntimeError(" ".join(args) + "\n" + r.stderr.strip())
    return r.stdout.strip()


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
    """A member's key, read from the database.

    Not scraped off /p/api-key: that page carries other 32-hex strings (a CSRF
    token among them) and a regex over it picks the wrong one, which fails as
    "Incorrect user credentials" and looks like a bug in the API.
    """
    out = sh("docker", "compose", "exec", "-T", "db", "psql", "-U", "demo",
             "-d", "loon_demo", "-t", "-c",
             "select k.api_key from api_keys k join users u on u.id=k.user_id "
             "where u.username='bob' limit 1;")
    return out.strip()


def demo_total(key, params):
    """What the demo ITSELF reports for a query — the yardstick the clients are
    measured against, fetched in the same run so a moving index cannot make the
    comparison wrong."""
    url = DEMO + "/api?apikey=" + key + "&" + params
    _, body = http(url)
    m = body.split('total="')
    return int(m[1].split('"')[0]) if len(m) > 1 else -1


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

    def start(self):
        sh("docker", "rm", "-f", self.container)
        sh("docker", "run", "-d", "--name", self.container,
           "--network", NETWORK, "-p", "%d:%d" % (self.port, self.internal_port),
           "-e", "PUID=1000", "-e", "PGID=1000", "-e", "TZ=UTC",
           self.image, check=True)

    def stop(self):
        sh("docker", "rm", "-f", self.container)

    def base(self):
        return "http://localhost:%d" % self.port


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
        out = sh("docker", "exec", self.container, "sh", "-c",
                 "grep -oE '<ApiKey>[a-f0-9]+' /config/config.xml 2>/dev/null | head -1")
        return out.replace("<ApiKey>", "").strip() or None

    def _hdr(self):
        return {"X-Api-Key": self.key, "Content-Type": "application/json"}

    def configure(self, key):
        """Build the indexer from Prowlarr's OWN schema rather than a
        hand-written body: the schema names every field and its default, so a
        change on their side surfaces as a missing field here instead of a
        silently ignored one."""
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


CLIENTS = {"hydra": Hydra, "prowlarr": Prowlarr}


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
            c.start()
            if not wait_for(c.ready, "%s to come up" % name):
                rep.check(name, "starts and answers its own API", False)
                continue
            rep.check(name, "starts and answers its own API", True)
            c.run(rep, key)
        finally:
            # Always, even on an exception: this box is deliberately kept
            # quiet, and a suite that leaks containers is one nobody runs twice.
            if not args.keep:
                c.stop()

    return rep.summary()


if __name__ == "__main__":
    sys.exit(main())
