#!/usr/bin/env python3
"""SEAMS.md against the registry keys the code actually has.

    python scripts/audit_seams.py

SEAMS.md is one of three governing documents, and the only one whose subject --
what is registered on the extension registry -- is mechanically checkable. It
already knows it drifts. Its own words:

    "It was 41 when this document was written and nobody noticed it drifting
     to 52 -- eleven contracts arrived and none of them reached the catalogue
     below, which is the exact failure this page exists to prevent."

It even ships the one-line grep that recounts. Nothing ran it, which is why
the number was restated three times in two weeks. This runs it, and two more.

WHAT IS CHECKED
---------------
  A  The stated contract count matches the constants that exist.
  B  Every DECLARED contract (an exported ...Name / ...Prefix in pluginapi)
     appears in the catalogue. This is the "eleven arrived, none reached the
     catalogue" failure, and it is the whole reason the page exists.
  C  Every registry key the code uses is NAMED somewhere in SEAMS.md -- the
     catalogue for a declared contract, or the deprecated block for a bare
     string. A seam nobody wrote down is one nobody can find.
  D  Every TYPE the Contract column names exists in pluginapi. This was the
     blind spot the first version wrote down, and it turned out to be worth
     closing rather than documenting: `usenet.series` was catalogued against
     `SeriesStore`, a type that has never existed in this codebase under that
     name -- the interface is `SeriesIndex`. Two more cells named the
     CONSTANT where the column means the TYPE.

WHAT IS STILL NOT CHECKED, AND WHY
----------------------------------
Whether the named type is the one actually REGISTERED under that key, and
whether the third column's prose is true. D catches a name that resolves to
nothing, which is the cheap half and the half that was wrong three times.
Following the value through a Register call to its concrete type means
type-checking Go from Python, and a check that guesses is worse than one whose
blind spot is written down.
"""

import io
import os
import re
import sys

PLUGINS = os.environ.get("LOON_PLUGINS", "../loon-plugins")
SEAMS = os.path.join(PLUGINS, "SEAMS.md")
TREES = ["../loon", PLUGINS, ".", "../loon-baseline"]

# An exported registry-key constant: the definition of a DECLARED contract.
CONST = re.compile(r'\b([A-Z][A-Za-z0-9_]*(?:Name|Prefix))\s*=\s*"([^"]+)"')
# Any constant that LOOKS like a registry key, wherever it lives. This is how
# the middle tier is found: a named constant in the PROVIDER's package, which
# a consumer can only reach by importing the provider or retyping the string.
ANY_CONST = re.compile(r'\b([A-Za-z_][A-Za-z0-9_]*)\s*=\s*"([a-z][a-z0-9]*(?:\.[a-z0-9-]+)+\.?)"')
# c.Register("literal", …) / c.Lookup("literal") / RegisterDef(…)
LITERAL = re.compile(r'\b(?:Register(?:Def)?|Lookup)\s*\(\s*"([^"]+)"')
# c.Register(SomeName, …) — resolved against the constants above.
IDENT = re.compile(r'\b(?:Register(?:Def)?|Lookup)\s*\(\s*(?:[A-Za-z_][A-Za-z0-9_]*\.)?([A-Z][A-Za-z0-9_]*)')

# | `key` / `.suffix` | `Contract` | For |
#
# The first cell ABBREVIATES: one full key, then bare suffixes sharing its
# namespace. Reading only the first token reports seven usenet ports as
# undocumented when the row documents all eight.
ROW = re.compile(r"^\|([^|]*)\|([^|]*)\|")
TOKEN = re.compile(r"`([^`]+)`")
# A Go type declaration in pluginapi, at top level or inside a type ( … ) block.
GO_TYPE = re.compile(r"^\s*type\s+([A-Za-z0-9_]+)|^	([A-Z][A-Za-z0-9_]*)\s+(?:interface|func|struct)\b", re.M)
# A bare Go identifier: `Catalog`, not `cache.PrefixDeleter` and not prose.
BARE_IDENT = re.compile(r"^[A-Za-z][A-Za-z0-9_]*$")
STATED = re.compile(r"There are \*\*(\d+)\*\* of them")

SKIP_DIR = {".git", "vendor", "node_modules", "__pycache__", ".claude"}


def catalogue(text):
    """Every key the catalogue tables name, with suffix rows expanded.

    Returns (keys, contracts) where contracts is [(key, type-name)] for
    every bare identifier in the second column.
    """
    keys, contracts, ns = set(), [], None
    for line in text.split(chr(10)):
        m = ROW.match(line)
        if not m:
            ns = None
            continue
        ns = None
        row_keys = []
        for tok in TOKEN.findall(m.group(1)):
            if tok.startswith('.'):
                if ns:
                    keys.add(ns + tok)
                    row_keys.append(ns + tok)
            else:
                keys.add(tok)
                row_keys.append(tok)
                ns = tok.split('.', 1)[0]
        if not row_keys:
            continue
        for tok in TOKEN.findall(m.group(2)):
            tok = tok.strip().lstrip('*')
            if BARE_IDENT.match(tok):
                contracts.append((row_keys[0], tok))
    return keys, contracts


def pluginapi_types(root):
    """Every type name pluginapi declares."""
    out = set()
    d = os.path.join(root, 'pluginapi')
    if not os.path.isdir(d):
        return out
    for n in sorted(os.listdir(d)):
        if not n.endswith('.go') or n.endswith('_test.go'):
            continue
        try:
            b = io.open(os.path.join(d, n), encoding='utf-8').read()
        except (OSError, UnicodeDecodeError):
            continue
        for x, y in GO_TYPE.findall(b):
            out.add(x or y)
    return out

BLOCK_COMMENT = re.compile('/\*.*?\*/', re.S)
LINE_COMMENT = re.compile('//[^' + chr(92) + 'n]*')


def strip_comments(text):
    """Blank out comments, keeping length so nothing else shifts.

    loon/core/extensions.go documents the registry with an EXAMPLE --
    c.Register("wiki.render", renderer) -- inside a doc comment. Scanning it
    reported wiki.render as an undocumented seam, which is a check inventing
    work: there is no such seam, only a sentence explaining what one looks
    like. The same trick audit_branding uses, and for the same reason.
    """
    text = BLOCK_COMMENT.sub(lambda m: ' ' * len(m.group(0)), text)
    return LINE_COMMENT.sub(lambda m: ' ' * len(m.group(0)), text)


def go_files():
    for tree in TREES:
        if not os.path.isdir(tree):
            continue
        for base, dirs, names in os.walk(tree):
            dirs[:] = [d for d in dirs if d not in SKIP_DIR]
            for n in names:
                # Test fixtures invent capability names on purpose -- a.b,
                # plain.thing, test.thing. -- and documenting them would be
                # documenting the tests.
                if not n.endswith(".go") or n.endswith("_test.go"):
                    continue
                p = os.path.join(base, n)
                try:
                    yield p, strip_comments(io.open(p, encoding="utf-8").read())
                except (OSError, UnicodeDecodeError):
                    continue


def covered(key, keys):
    """A prefix entry covers everything beneath it, in either direction."""
    for k in keys:
        if k == key:
            return True
        if (k.endswith(".") or k.endswith(".*")) and key.startswith(k.rstrip("*")):
            return True
        if (key.endswith(".") or key.endswith(".*")) and k.startswith(key.rstrip("*")):
            return True
    return False


def main():
    if not os.path.isfile(SEAMS):
        # Skipping, exit 0, and printed loudly -- the same contract
        # audit_capabilities uses for the same reason: loon-plugins is a
        # separate checkout and a bare clone of the host has no sibling to
        # read. NOT the same as the empty-route-list case in audit_adminnav,
        # which meant "the source is there and said nothing".
        print("seams: loon-plugins not beside this checkout; skipping")
        return 0
    text = io.open(SEAMS, encoding="utf-8").read()
    listed, contracts = catalogue(text)

    declared, anyconst, used = {}, {}, {}
    for path, body in go_files():
        rel = path.replace(os.sep, "/").lstrip("./")
        if "/pluginapi/" in rel:
            for name, val in CONST.findall(body):
                # A registry key is namespaced: it has a dot. SlotName = "name"
                # ends in Name and is a slot ATTRIBUTE, not a seam, and counting
                # it is what made this report 60 against the document's 59 --
                # the recount and the number have to be the same question.
                if "." not in val:
                    continue
                declared[val] = (name, rel)
        for name, val in ANY_CONST.findall(body):
            anyconst.setdefault(name, (val, rel))
        for val in LITERAL.findall(body):
            if re.match(r"^[a-z][a-z0-9]*(\.[a-z0-9-]+)+\.?$", val):
                used.setdefault(val, rel)
        for name in IDENT.findall(body):
            if name in anyconst:
                used.setdefault(anyconst[name][0], rel)

    failed = False

    # A. the count the document states about itself
    m = STATED.search(text)
    actual = len(set(v for v in declared))
    if not m:
        failed = True
        print("  NO STATED COUNT  SEAMS.md no longer says how many contracts "
              "there are; the recount cannot be checked.")
    elif int(m.group(1)) != actual:
        failed = True
        print("  COUNT DRIFTED    SEAMS.md says %s declared contracts; "
              "pluginapi has %d." % (m.group(1), actual))
        print("                   This is the drift the page's own warning "
              "describes. Recount, then fix the catalogue.")

    # B. declared contracts that never reached the catalogue
    for val, (name, rel) in sorted(declared.items()):
        if not covered(val, listed):
            failed = True
            print("  NOT CATALOGUED   %-30s %s (%s)" % (val, name, rel))
            print("                   a declared contract absent from the "
                  "catalogue -- the exact failure SEAMS.md exists to prevent")

    # C. keys the document does not name at all
    for val, rel in sorted(used.items()):
        if covered(val, listed) or val in declared:
            continue
        if val in text:
            continue          # named in prose, e.g. the deprecated block
        failed = True
        print("  UNDOCUMENTED     %-30s %s" % (val, rel))
        print("                   a registry key SEAMS.md never mentions")

    # D. a Contract column naming a type that does not exist
    types = pluginapi_types(PLUGINS)
    if not types:
        print("  NO TYPES FOUND   pluginapi has no type declarations; check D "
              "would pass by finding nothing to check.")
        failed = True
    else:
        for key, t in contracts:
            if t not in types:
                failed = True
                print("  NO SUCH TYPE     %-30s Contract column says %r" % (key, t))
                print("                   pluginapi declares no such type -- the "
                      "entry names something that does not exist")

    print()
    print("seams: %d catalogue keys, %d declared contracts, %d registry keys in code"
          % (len(listed), actual, len(used)))
    if failed:
        return 1
    print("seams: the catalogue matches the code, and the stated count is the real one.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
