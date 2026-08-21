"""Find CSS classes the templates use and no stylesheet defines.

This is the contract audit (contracts_web.go) in another medium. Same shape of
bug: two halves, one filled, and nothing anywhere reporting the gap. A class
that does not exist has no effect and raises no error, so the element renders as
though the class were absent -- indistinguishable from it being styled to look
plain.

Not hypothetical. `.button--danger` and `.text-danger` were used across the
forum, communities, admin and host templates and were defined in NO stylesheet,
so every Delete, Remove and Clear on the site rendered identically to the safe
button beside it, for the life of the codebase.

Static: reads files, needs no running site, so it can run before the build
rather than after.

    python scripts/audit_css.py

Exits non-zero when anything is found.
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# Directories no walk here should descend into. Named once, because the JS
# check below reads the PLUGIN tree as well as this one and that tree brings
# vendor/ and node_modules/ with it.
SKIP_DIRS = {".git", "node_modules", "__pycache__", "vendor", "clonecheck",
             "examples", "docs"}
TEMPLATES = os.path.join(ROOT, "web", "templates")
STYLES = os.path.join(ROOT, "web", "static", "css")

# Stands in for a template action while class attributes are matched. A byte
# that cannot appear in HTML, so a token still carrying it is one whose real
# value is only decided at render time.
MARK = chr(0)

# Classes applied at runtime rather than written in a template. Listed with a
# reason each, because an ignore list without them becomes the place findings go
# to die.
RUNTIME = {
    # site_chrome.html builds these by concatenation ('pw-meter--' + band), so
    # they are never literals anywhere.
    "pw-field", "pw-field__reveal", "pw-meter", "pw-meter__track", "pw-meter__text",
    "pw-meter--short", "pw-meter--weak", "pw-meter--fair", "pw-meter--good",
    "pw-meter--strong",
    # Toggled by the dropdown and Bootstrap tab shims in site_chrome.html.
    "active", "show", "fade",
    # ...and the two the same shim SELECTS on. site_chrome.html is the one
    # script that drives markup it does not contain: the tab strip it operates
    # is rendered by plugins, so the JS check cannot find .nav or .tab-pane in
    # the host's own template directory and would report a dead selector on
    # every run. They are the shim's contract with every plugin that uses tabs.
    "nav", "tab-pane",

    # UNIT3D PARITY MARKERS, not styling hooks. UNIT3D renders each home-page
    # block as <section class="panelV2 blocks__<name>">, and these carry the
    # same names so a panel here is identifiable as the block it corresponds
    # to. They are deliberately unstyled -- panelV2 does the looking -- and
    # templates_test.go asserts them by name to check the home page renders the
    # blocks the host ordered.
    "blocks__featured", "blocks__latest-releases", "blocks__latest-topics",
    "blocks__no-releases", "blocks__popular", "blocks__top-groups",
    "blocks__top-posters", "blocks__widget",

    # The same convention on /achievements, though nothing asserts these: they
    # document which UNIT3D panel each section mirrors. Kept for consistency
    # with the blocks above -- deleting three names to satisfy a linter written
    # in this repo would be the tail wagging the dog.
    "achievements__unlocked", "achievements__pending", "achievement__statistics",
    # ...and the fourth, which was simply missed from the list above when it
    # was written. Same panel, same convention, same reason.
    "achievements__progress",

    # base.html names its two widget regions on the <aside> itself:
    #
    #     <aside class="sidebar sidebar--left">
    #
    # .sidebar does the layout and the modifiers style nothing, deliberately —
    # they say WHICH region this is, which is the only thing distinguishing two
    # otherwise identical asides in the DOM. Worth keeping for anyone reading
    # the markup or writing a selector against one side, and worth naming here
    # so the next person does not delete them as dead.
    "sidebar--left", "sidebar--right",
}


def used():
    """Every literal class name written in a template.

    Template actions are blanked to MARK BEFORE class attributes are matched,
    which two separate problems depend on:

      1. An action can contain a double quote -- {{if eq .Path "/x"}} -- and
         matching class="([^"]*)" first ends the attribute in the middle of it.
         That produced findings for .hasPrefix, .eq and .or, which are template
         functions, not classes.
      2. A class can be BUILT from an action: poster--h{{hue .Name}} is one
         token, not a class called poster--h.
    """
    out = {}
    for dirpath, _dirs, files in os.walk(TEMPLATES):
        for fn in files:
            if not fn.endswith(".html"):
                continue
            full = os.path.join(dirpath, fn)
            rel = os.path.relpath(full, ROOT).replace(os.sep, "/")
            with open(full, encoding="utf-8") as fh:
                text = fh.read()
            text = re.sub(r"\{\{.*?\}\}", MARK, text, flags=re.S)
            for value in re.findall(r'class="([^"]*)"', text):
                for cls in value.split():
                    if MARK in cls:
                        continue  # built at render time
                    if not re.fullmatch(r"[A-Za-z][A-Za-z0-9_-]*", cls):
                        continue
                    out.setdefault(cls, set()).add(rel)
    return out


def defined():
    """Every class name any stylesheet writes a rule for.

    INLINE <style> BLOCKS COUNT TOO. A standalone page that carries its own
    rules is styling its classes just as much as a .css file is, and reading
    only the stylesheets reported six of dev_compare.html's own classes --
    .pane, .stage, .box, .hint, .primary, .sel -- as undefined while the rules
    for them sat forty lines above the markup. Six false positives out of
    eleven findings is enough to make the whole list look untrustworthy, which
    is how a check stops being run.
    """
    out = set()
    for dirpath, _dirs, files in os.walk(TEMPLATES):
        for fn in files:
            if not fn.endswith(".html"):
                continue
            with open(os.path.join(dirpath, fn), encoding="utf-8") as fh:
                text = fh.read()
            for block in re.findall(r"<style[^>]*>(.*?)</style>", text, re.S | re.I):
                block = re.sub(r"/\*.*?\*/", " ", block, flags=re.S)
                for cls in re.findall(r"\.(-?[A-Za-z_][A-Za-z0-9_-]*)", block):
                    out.add(cls)
    for dirpath, _dirs, files in os.walk(STYLES):
        for fn in files:
            if not fn.endswith(".css"):
                continue
            with open(os.path.join(dirpath, fn), encoding="utf-8") as fh:
                text = fh.read()
            # Comments stripped first, so a class mentioned only in prose does
            # not count as defined. That is the exact false negative that would
            # let the next .button--danger through.
            text = re.sub(r"/\*.*?\*/", " ", text, flags=re.S)
            for cls in re.findall(r"\.(-?[A-Za-z_][A-Za-z0-9_-]*)", text):
                out.add(cls)
    return out



# ── classes JavaScript reaches for ──────────────────────────────────────────
#
# A CLASS RENAME HAS TWO HALVES. Converting .card-header to .panel__header in
# the markup leaves every selector pointing at the old name; the handler finds
# nothing, and the page still LOOKS converted. Nothing else in the stack
# notices — not the template parser, not a test that renders the page, not a
# screenshot.
#
# The test is deliberately narrow. Two wider ones were tried and rejected:
#
#   "a JS class with no CSS rule" flags .js-group-toggle, .btn-vote,
#   .dismiss-btn — handles that are never meant to be styled. 38 findings, all
#   noise, which is how a check gets switched off rather than obeyed.
#
#   "a JS class absent from the markup" flags .open, .expanded, .selected,
#   .voted — state classes the script ADDS at runtime, which correctly appear
#   in no template.
#
# What is left: a class the script SEARCHES for, that no markup carries and no
# script adds. That is a selector addressing nothing.

# Reads: querySelector('.a .b'), closest('.a'), classList.contains('a')
JS_SEARCH = [
    (re.compile(r"""(?:querySelector(?:All)?|closest|matches)\(\s*['"]([^'"]+)['"]"""), True),
    (re.compile(r"""classList\.(?:contains|remove)\(\s*['"]([^'"]+)['"]"""), False),
    (re.compile(r"""getElementsByClassName\(\s*['"]([^'"]+)['"]"""), False),
]
# Writes: anything that can PUT the class on an element.
JS_ADD = [
    re.compile(r"""classList\.(?:add|toggle|replace)\(\s*['"]([^'"]+)['"](?:\s*,\s*['"]([^'"]+)['"])?"""),
    re.compile(r"""className\s*=\s*['"]([^'"]*)['"]"""),
    re.compile(r"""className\s*=\s*['"]([^'"]*)['"]\s*\+"""),
    re.compile(r"""['"]([\w-]+)['"]\s*:\s*['"]?"""),
]
SCRIPT_BLOCK = re.compile(r"<script\b[^>]*>(.*?)</script>", re.S | re.I)
SEL_CLASS = re.compile(r"\.([a-zA-Z_][\w-]*)")
BARE_CLASS = re.compile(r"^[a-zA-Z_][\w-]*$")


def _js_names(text, pats, selector_syntax=None):
    out = set()
    for entry in pats:
        pat, is_sel = entry if isinstance(entry, tuple) else (entry, False)
        for m in pat.finditer(text):
            for g in m.groups():
                if not g:
                    continue
                if is_sel:
                    out.update(SEL_CLASS.findall(g))
                else:
                    for tok in g.split():
                        if BARE_CLASS.match(tok):
                            out.add(tok)
    return out


def js_selectors(trees):
    """Classes a script looks for that its own templates never provide.

    Returns [(tree_label, relative_path, class_name)].

    SCOPED BY DIRECTORY, and that is the whole accuracy of it. A tree-wide
    "does any markup carry this" missed the bug this exists for: usenet asked
    for .badge after its own markup moved to .tag, and other plugins still
    write class="badge", so the tree-wide set still contained it. A script in
    usenet/templates addresses usenet's markup — tight enough to notice a class
    that left THIS plugin, loose enough for one template's script to drive a
    sibling fragment, which is how usenet's tabs work.
    """
    per_dir_markup, per_dir_added, scripts = {}, {}, []
    for label, root in trees:
        for dirpath, dirs, files in os.walk(root):
            dirs[:] = [d for d in dirs if d not in SKIP_DIRS]
            for fn in files:
                if not fn.endswith(".html"):
                    continue
                path = os.path.join(dirpath, fn)
                with open(path, encoding="utf-8", errors="replace") as fh:
                    text = fh.read()
                mk = per_dir_markup.setdefault(dirpath, set())
                for attr in re.findall(r"""class\s*=\s*["']([^"']*)["']""", text):
                    # A template action inside the attribute is blanked: half of
                    # `{{if .X}}a{{else}}b` is not a class name.
                    for tok in re.sub(r"\{\{.*?\}\}", " ", attr, flags=re.S).split():
                        mk.add(tok)
                ad = per_dir_added.setdefault(dirpath, set())
                for block in SCRIPT_BLOCK.findall(text):
                    ad.update(_js_names(block, JS_ADD))
                    scripts.append((label, dirpath,
                                    os.path.relpath(path, root).replace(os.sep, "/"),
                                    _js_names(block, JS_SEARCH)))
    out = []
    for label, dirpath, rel, names in scripts:
        mk = per_dir_markup.get(dirpath, set())
        ad = per_dir_added.get(dirpath, set())
        for cls in sorted(names):
            if cls in mk or cls in ad or cls in RUNTIME:
                continue
            out.append((label, rel, cls))
    return out

# The trees whose scripts are checked. The PLUGIN tree is here and not in the
# class check above, deliberately: pointing `used()` at it surfaces 310
# undefined names in one go, most of them Bootstrap utilities the host has
# never shimmed, and that is a body of work rather than a check. The JS check
# has no such backlog — it reported zero on the day it was written — so it can
# be wired straight in and stay green.
JS_TREES = [
    ("host", os.path.join(ROOT, "web")),
    ("plugins", os.path.join(os.path.dirname(ROOT), "loon-plugins")),
]


def main():
    use, have = used(), defined()
    missing = sorted(c for c in use if c not in have and c not in RUNTIME)

    dead = js_selectors([(l, p) for l, p in JS_TREES if os.path.isdir(p)])
    if dead:
        print("css: %d JavaScript selector(s) addressing a class no markup carries\n"
              % len(dead))
        for label, rel, cls in dead:
            print("   %-9s %-48s .%s" % (label, rel, cls))
        print("")
        print("A class rename has two halves. Converting .badge to .tag in the markup")
        print("leaves querySelector('td .badge') behind, the handler finds nothing, and")
        print("the page still LOOKS converted -- no template error, no failing test, no")
        print("difference in a screenshot of the loaded page.")
        print("")
        print("css: %d dead JavaScript selector(s)" % len(dead))
        return 1

    if not missing:
        print("css: 0 undefined classes (%d used, all defined), 0 dead JS selectors"
              % len(use))
        return 0

    print("css: %d class(es) used in templates and defined in no stylesheet" % len(missing))
    for cls in missing:
        where = sorted(use[cls])
        extra = " (+%d more)" % (len(where) - 3) if len(where) > 3 else ""
        print("   .%-26s %s%s" % (cls, ", ".join(where[:3]), extra))
    print("")
    print("Each renders as though the class were absent. Not always a bug -- a hook")
    print("for JS or a plugin's own stylesheet is legitimate -- but each should be a")
    print("decision rather than a typo. Add real ones to RUNTIME with a reason.")
    print("")
    # Summary LAST, so a caller showing only the final line (deploy.sh) gets the
    # verdict rather than the tail of the advice.
    print("css: %d undefined class(es) used in templates" % len(missing))
    return 1


if __name__ == "__main__":
    sys.exit(main())
