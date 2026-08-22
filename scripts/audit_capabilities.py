"""Find capabilities a wired plugin asks for that no wired plugin provides.

This is the seventh instance of one bug. A plugin consumes a peer's capability
with an OPTIONAL lookup:

    if v, ok := c.Lookup(pluginapi.ScheduledEventsName); ok { ... }

and degrades quietly when it is absent -- correctly, because a host that has not
installed the provider is a legitimate configuration. The cost of that
correctness lands on the host, which gets no signal at all. Rewards ran without
event gating for as long as the events plugin went unwired, and what finally
noticed was the LINK CRAWLER seeing a 404, not anything looking at capabilities.

contracts_web.go checks reward payouts, achievement metrics and the identity
view at runtime. It cannot check this: the registry can tell you a name is
absent but not whether anybody wanted it, and "absent and unwanted" is the
normal case for most of these. The missing half of the question lives in source.

So this is STATIC, and it answers the question in one pass:

    consumed by a plugin the host wires
      + provided only by a plugin the host does NOT wire   -> finding

    python scripts/audit_capabilities.py

Exits non-zero when anything is found.
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PLUGINS = os.path.normpath(os.path.join(ROOT, "..", "loon-plugins"))
# Every host .go file, not just main.go. Plugins are imported wherever their
# wiring lives -- forum in forum_web.go, store in store_web.go -- and reading
# only main.go under-reports what is enabled. The tool's own "consumed by
# nothing" list is what caught that: it claimed ranks.granter had no consumer
# while store was sitting right there consuming it.
HOST = ROOT

# The host registers and consumes capabilities like a plugin, so it needs a name
# in the same space. It is always "wired".
HOST_NAME = "host"

# const XName = "registry.key"
CONST = re.compile(r'\b([A-Z][A-Za-z0-9_]*Name)\s*=\s*"([^"]+)"')
# c.Register(pluginapi.XName, …) / RegisterDef(… pluginapi.XName …)
PROVIDES = re.compile(r'Register(?:Def)?\s*\([^)]*?pluginapi\.([A-Za-z0-9_]*Name)')
# c.Lookup(pluginapi.XName)
#
# BLIND SPOT, written down rather than guessed at. This sees a lookup only when
# it names a pluginapi CONSTANT. A bare-string lookup — c.Lookup("achievements.
# icons") — is invisible, and four of those exist: achievements.icons, .files,
# .l10n.slugs and .l10n.resolve, all in one file and all listed as deprecated
# bare-string conventions in loon-plugins/SEAMS.md.
#
# Probed 22 Aug 2026 by adding c.Lookup("probe.capability.nothing.provides") to
# a wired plugin: this reported "0 unfilled", as designed. Widening the pattern
# to any string would mean guessing which literals are registry keys, and the
# rule this repo follows is that a check with a written-down blind spot beats
# one that guesses. The four are the whole of it; converting them to constants
# is what closes this, not a cleverer regex.
CONSUMES = re.compile(r'Lookup\s*\(\s*pluginapi\.([A-Za-z0-9_]*Name)')
# The host's plugin imports, blank or named.
IMPORT = re.compile(r'"github\.com/the-loon-clan/loon-plugins/([a-z0-9_]+)"')


def go_files(root):
    for dirpath, _dirs, files in os.walk(root):
        if os.sep + "." in dirpath:
            continue
        for fn in files:
            if fn.endswith(".go") and not fn.endswith("_test.go"):
                yield os.path.join(dirpath, fn)


def plugin_of(path):
    """The plugin directory a file belongs to, relative to loon-plugins."""
    rel = os.path.relpath(path, PLUGINS)
    return rel.split(os.sep)[0]


def scan():
    """Who declares, provides and consumes each capability.

    The HOST is scanned as a provider too, under the name "host". It is not a
    plugin but it registers capabilities like one -- invites.granter is
    published by invites_web.go, because invites live on the host's users table
    and no plugin can own them. Reading only loon-plugins reported that as
    "provided by nothing" and the store as silently degraded, which was exactly
    backwards.

    Providers are also collected from plugins that are NOT wired, so a finding
    can say "the thing you want exists over there, unwired" rather than the far
    less useful "missing"."""
    names, provides, consumes = {}, {}, {}
    for path in go_files(PLUGINS):
        with open(path, encoding="utf-8", errors="replace") as fh:
            text = fh.read()
        plugin = plugin_of(path)
        if plugin == "pluginapi":
            for const, key in CONST.findall(text):
                names[const] = key
        for const in PROVIDES.findall(text):
            provides.setdefault(const, set()).add(plugin)
        for const in CONSUMES.findall(text):
            consumes.setdefault(const, set()).add(plugin)

    for path in go_files(HOST):
        if os.sep + "scripts" + os.sep in path:
            continue
        with open(path, encoding="utf-8", errors="replace") as fh:
            text = fh.read()
        for const in PROVIDES.findall(text):
            provides.setdefault(const, set()).add(HOST_NAME)
        for const in CONSUMES.findall(text):
            consumes.setdefault(const, set()).add(HOST_NAME)
    return names, provides, consumes


def wired():
    """Plugins this host imports, across every file. A blank import is the whole
    wiring for a self-registering plugin, so the import set IS the enabled set."""
    out = set()
    for path in go_files(HOST):
        if os.sep + "scripts" + os.sep in path:
            continue
        with open(path, encoding="utf-8", errors="replace") as fh:
            out.update(IMPORT.findall(fh.read()))
    return out


def main():
    if not os.path.isdir(PLUGINS):
        print("capabilities: loon-plugins not beside this checkout; skipping")
        return 0

    names, provides, consumes = scan()
    on = wired() | {HOST_NAME}
    findings, unused = [], []

    for const, users in sorted(consumes.items()):
        key = names.get(const, const)
        # Only plugins that are actually wired here can be hurt by an absence.
        live_users = sorted(u for u in users if u in on)
        if not live_users:
            continue
        providers = provides.get(const, set())
        if providers & on:
            continue
        findings.append((key, const, live_users, sorted(providers)))

    # Reported separately and never as a fault: a provider nobody consumes is a
    # seam waiting for its consumer, which is a normal state for a framework.
    for const, ps in sorted(provides.items()):
        if (ps & on) and not (consumes.get(const, set()) & on):
            unused.append((names.get(const, const), sorted(ps & on)))

    for key, const, users, providers in findings:
        print("  %s" % key)
        print("     wanted by : %s" % ", ".join(users))
        if providers:
            print("     provided by: %s (not wired into this host)" % ", ".join(providers))
            print("     fix        : import it in main.go, or accept the degraded behaviour")
        else:
            print("     provided by: nothing in loon-plugins")
            print("     fix        : nothing implements this yet; the consumer degrades silently")

    if unused:
        print("  offered here and consumed by nothing (not a fault):")
        for key, ps in unused:
            print("     %-24s from %s" % (key, ", ".join(ps)))

    print("capabilities: %d unfilled across %d wired plugin(s)" % (len(findings), len(on)))
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main())
