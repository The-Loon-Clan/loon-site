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
    # ...and the PREFIX itself, which is what the concatenation site literally
    # writes: classList.add('pw-meter--' + band). The state check reads the
    # string in the source, not the value at runtime, so it sees the stem.
    "pw-meter--",
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


def used(root=None, rel_to=None):
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
    for dirpath, dirs, files in os.walk(root or TEMPLATES):
        dirs[:] = [d for d in dirs if d not in SKIP_DIRS]
        for fn in files:
            if not fn.endswith(".html"):
                continue
            full = os.path.join(dirpath, fn)
            rel = os.path.relpath(full, rel_to or ROOT).replace(os.sep, "/")
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


# A plugin's CSS lives in a Go string now, not a <style> block.
#
# The RegisterStylesheet migration moved every plugin's rules out of its
# templates and into <plugin>/stylesheet.go, so the host can serve them from a
# URL with a hash and a year of caching -- and so script-src could drop
# 'unsafe-inline'. Sixteen plugins moved.
#
# This audit read .html and .css and nothing else, so the day that landed it
# started reporting seven classes as styled by nobody -- .chat-msg, .chat-user,
# .expanded, .voted and three more -- every one of which has a rule, sitting in
# a file the audit could not see. The rules never moved; the reader did.
#
# That is the worse half of this failure mode. A check that misses a real
# problem is quiet; a check that invents seven is read once, disbelieved, and
# then ignored along with the real finding it reports next week.
STYLESHEET_GO = "stylesheet.go"


def _go_stylesheet_classes(path):
    """Class names the CSS constants in a Go file write rules for.

    Go raw strings cannot contain a backtick, so the delimiters are unambiguous
    and this needs no Go parser.
    """
    out = set()
    with open(path, encoding="utf-8", errors="replace") as fh:
        text = fh.read()
    for block in re.findall(chr(96) + "([^" + chr(96) + "]*)" + chr(96), text, re.S):
        block = re.sub(r"/\*.*?\*/", " ", block, flags=re.S)
        for cls in re.findall(r"\.(-?[A-Za-z_][A-Za-z0-9_-]*)", block):
            out.add(cls)
    return out


def inline_styles(root):
    """Class names the stylesheets under `root` write rules for.

    Both kinds: the <style> blocks still in templates, and the CSS constants in
    <plugin>/stylesheet.go, which is where sixteen plugins' rules now live. See
    STYLESHEET_GO above for what reading only the first kind cost.

    SCOPED, and that is the whole point. These used to be pooled across both
    trees, so a rule in the forum's stylesheet counted as defining that class
    for lists and roadmap -- whose pages never load it. Writing one plugin's
    stylesheet moved two other plugins' baselines without touching them.

    A page gets the host's stylesheets plus its OWN plugin's rules. Nothing
    else.
    """
    out = set()
    if not os.path.isdir(root):
        return out
    for dirpath, dirs, files in os.walk(root):
        dirs[:] = [d for d in dirs if d not in SKIP_DIRS]
        for fn in files:
            path = os.path.join(dirpath, fn)
            if fn == STYLESHEET_GO:
                out |= _go_stylesheet_classes(path)
                continue
            if not fn.endswith(".html"):
                continue
            with open(path, encoding="utf-8") as fh:
                text = fh.read()
            for block in re.findall(r"<style[^>]*>(.*?)</style>", text, re.S | re.I):
                block = re.sub(r"/\*.*?\*/", " ", block, flags=re.S)
                for cls in re.findall(r"\.(-?[A-Za-z_][A-Za-z0-9_-]*)", block):
                    out.add(cls)
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
    out = inline_styles(TEMPLATES)
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
# The writes that put a class on an element AS A CLASS. A subset of JS_ADD,
# which also has to catch the loose shapes a class name can arrive in.
JS_STATE = [
    re.compile(r"""classList\.(?:add|toggle|replace)\(\s*['"]([^'"]+)['"](?:\s*,\s*['"]([^'"]+)['"])?"""),
    re.compile(r"""className\s*=\s*['"]([^'"]*)['"]"""),
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

def js_states(trees):
    """Classes a script ADDS that no stylesheet defines.

    Returns [(tree_label, relative_path, class_name)].

    The mirror of js_selectors(). That one catches a class a script looks for
    and the markup never carries; this catches a class a script puts ON an
    element that no rule will ever match -- a state change with no visible
    effect, which is indistinguishable from a state that is simply never
    reached.

    Scoped the same way defined() is: the host's stylesheets plus the <style>
    blocks of the plugin the script lives in. A rule in another plugin cannot
    style this one's markup.

    EXCLUDED: a class that is also read back (classList.contains, querySelector
    and friends). Those are JavaScript markers with no visual job, and flagging
    them would bury the ones that mean something.
    """
    host_css = defined()
    out = []
    # Two classes in one file belonging to the other workstream. A baseline
    # rather than a red build, and the entry IS the handover: the requests
    # page toggles .bg-opacity-50 and .text-white, both Bootstrap names this
    # site defines nowhere, so its selection highlight does not highlight.
    allowed = {("requests/templates/community_requests.html", "bg-opacity-50"),
               ("requests/templates/community_requests.html", "text-white")}
    for label, root in trees:
        for dirpath, dirs, files in os.walk(root):
            dirs[:] = [d for d in dirs if d not in SKIP_DIRS]
            added, searched, files_with = {}, set(), {}
            for fn in sorted(files):
                if not fn.endswith(".html"):
                    continue
                path = os.path.join(dirpath, fn)
                with open(path, encoding="utf-8", errors="replace") as fh:
                    text = fh.read()
                for block in SCRIPT_BLOCK.findall(text):
                    rel = os.path.relpath(path, root).replace(os.sep, "/")
                    # JS_STATE, not JS_ADD. JS_ADD's loosest pattern matches an
                    # object-literal KEY -- {'Content-Type': ...}, {allowEval:
                    # false} -- which is right for "could this put a class on an
                    # element" and useless here: 60 of the first 78 findings
                    # were request headers and htmx config.
                    for cls in _js_names(block, JS_STATE):
                        added.setdefault(cls, rel)
                    searched.update(_js_names(block, JS_SEARCH))
            if not added:
                continue
            # The plugin's own rules: this directory and its siblings under the
            # same plugin, which is where a plugin keeps a shared style partial.
            scope = os.path.dirname(dirpath) if os.path.basename(dirpath) == "templates" else dirpath
            have = host_css | inline_styles(scope)
            for cls, rel in sorted(added.items()):
                if cls in have or cls in searched or cls in RUNTIME:
                    continue
                if (rel, cls) in allowed:
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



# ── the plugin tree ─────────────────────────────────────────────────────────
#
# audit_css read only the host's templates for its whole life, so 108 plugin
# template files were never checked. That is how .btn-group, .btn-group-sm and
# .form-control-color sat in the ticket filters and the wiki colour input doing
# NOTHING -- each rendering as though the class were absent, which is
# indistinguishable from being styled to look plain. All three were found by
# accident while converting something else.
#
# CHECKLIST section 8 already required this: "templates speak the HOST's design
# vocabulary, or the host overrides them... If the plugin renders its own
# fragments, the README says which component names it assumes." The rule
# existed; nothing enforced it.
#
# A BASELINE, because 286 distinct names (373 counting each plugin that
# uses one) is not a list of typos. It is four things:
#
#   ~217  the plugin's own names, styled nowhere    (forum has 164 of them)
#    ~84  Bootstrap utilities the host never shimmed
#    ~22  Bootstrap Icons, and no font is shipped   (forum, all of them)
#     ~8  JS hooks, which are legitimate and belong in RUNTIME as they surface
#
# THE HOST SHIMS A SUBSET OF BOOTSTRAP, which is the thing to know before
# reading the utility findings: mb-3, d-flex, gap-2 and align-items-center are
# defined; mb-5, pt-3, ps-3, px-3, rounded, text-break and col-4 are not. A
# conversion that "keeps the utilities because the host defines them" is right
# about some of them and wrong about the rest, and this is what tells them
# apart.
#
# PER PLUGIN, NOT A TOTAL. A total lets a new plugin's findings hide behind
# another's cleanup. Keyed like this, a plugin with no entry fails on its FIRST
# undefined class, which is what stops the next one shipping like the forum's.
#
# Lower an entry in the same commit that fixes one. Measured 21 Aug 2026.
# WHAT IS LEFT, and why each entry is a number rather than a fix. Measured
# 22 Aug 2026, after three shim completions took this from 145 to 58.
#
#   requests 31   another workstream's plugin — see loon-plugins/HANDOVER.md,
#                 which names the command that reports each item.
#   forum 9       modifier hooks beside a base class that carries the styling
#                 (.forum-panel .forum-categories-panel). Kept so a host or a
#                 later variant can target one panel without a tag selector.
#                 Padding them with empty rules would buy the number and say
#                 nothing.
#   lists 5       host CONTRACT, not the plugin's to define: cover-card,
#                 cover-wrap, cover-img, cover-img-placeholder arrive with
#                 Deps.NzbCardCSS as a Go value at render time, so a static
#                 check cannot see them. lists/README.md now lists them, which
#                 is what CHECKLIST section 8 asks for. report-content is the
#                 trigger Deps.ReportModal's own script binds to.
#   the other 13  BEM names written in anticipation of a stylesheet nobody
#                 wrote — app-queue__item, fx-card__body, shots__item,
#                 inbox-shell and the rest. VERIFIED inert: no rule mentions
#                 them and no script selects them, so each element renders the
#                 same with the class as without.
#
# Those 13 were NOT deleted, and the reason is worth writing down. Removing
# them would take this number to near zero and change nothing a reader sees —
# optimising the measurement rather than the thing measured, which is the
# mistake this file exists to catch elsewhere. They are somebody's intended
# structure; whoever styles it adds the rule, and the entry goes down then.
PLUGIN_BASELINE = {
    "achievements": 1,
    "applications": 5,
    "cosmetics": 3,
    "donations": 1,
    "forum": 9,
    "lists": 5,
    "mediainfo": 1,
    "messages": 1,
    "offers": 1,
    # 0 since 23 Aug 2026: the one undefined name was .rg-bio, which the detail
    # template had been carrying while its font-size and line-height sat in a
    # style attribute beside it. Naming the class defined it.
    "releasegroups": 0,
    "requests": 31,
    "uploads": 1,
}

def main():
    use, have = used(), defined()
    missing = sorted(c for c in use if c not in have and c not in RUNTIME)

    trees = [(l, p) for l, p in JS_TREES if os.path.isdir(p)]
    blind = js_states(trees)
    if blind:
        print("css: %d class(es) JavaScript applies that no stylesheet defines\n"
              % len(blind))
        for label, rel, cls in blind:
            print("   %-9s %-48s .%s" % (label, rel, cls))
        print("")
        print("The element gets the class and no rule matches it, so the state change")
        print("is invisible: a reaction you left looks like one you did not, a picker")
        print("opens with display:none still winning. Nothing errors, and a screenshot")
        print("of the page BEFORE the click looks correct.")
        print("")
        print("css: %d state class(es) nothing styles" % len(blind))
        return 1

    dead = js_selectors(trees)
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

    plugin_root = os.path.join(os.path.dirname(ROOT), "loon-plugins")
    over, stale, ptotal = [], [], 0
    if os.path.isdir(plugin_root):
        pused = used(plugin_root, plugin_root)
        # A plugin's own <style> blocks, per plugin. Pooling them let one
        # plugin's stylesheet mark another's classes defined.
        own = {}
        for plugin in sorted(os.listdir(plugin_root)):
            d = os.path.join(plugin_root, plugin)
            if os.path.isdir(d) and plugin not in SKIP_DIRS:
                own[plugin] = inline_styles(d)
        pmissing = {}
        for c, where in pused.items():
            if c in have or c in RUNTIME:
                continue
            # A `js-` prefix DECLARES a behaviour hook: a class a script binds
            # to, with no appearance of its own. Exempt by convention, because
            # the convention is what makes the intent checkable — roadmap's
            # js-show-requests and usenet's js-group-toggle are hooks and say
            # so, while btn-delete and vote-count are ALSO bound by scripts and
            # very much want rules. "A script searches for it" is not the test;
            # a Delete button that looks like the safe button beside it is the
            # bug this file was written for.
            if c.startswith("js-"):
                continue
            # Keep only the plugins that use it AND do not define it themselves.
            w = [x for x in where if c not in own.get(x.split("/")[0], ())]
            if w:
                pmissing[c] = w
        # EVERY plugin that uses the class, not the first one alphabetically.
        # Attributing a shared name to one owner makes the baseline move when an
        # UNRELATED plugin is cleaned: converting messages took .btn-danger's
        # first user away and the count silently reappeared under uploads. A
        # baseline that shifts when somebody else tidies is not a baseline.
        per = {}
        for cls, where in pmissing.items():
            for w in where:
                per.setdefault(w.split("/")[0], set()).add(cls)
        per = {k: sorted(v) for k, v in per.items()}
        ptotal = len(pmissing)
        for plugin, names in sorted(per.items()):
            allowed = PLUGIN_BASELINE.get(plugin, 0)
            if len(names) > allowed:
                over.append((plugin, len(names), allowed, sorted(names)[:6]))
        for plugin, allowed in sorted(PLUGIN_BASELINE.items()):
            if len(per.get(plugin, [])) < allowed:
                stale.append((plugin, len(per.get(plugin, [])), allowed))

    if over:
        print("css: %d plugin(s) using more undefined classes than recorded\n" % len(over))
        for plugin, n, was, sample in over:
            print("   %-16s %d, baseline %d   %s" % (plugin, n, was, ", ".join("." + c for c in sample)))
        print("")
        print("css: %d plugin(s) over the baseline" % len(over))
        return 1
    if stale:
        print("css: baseline stale (fixed, so lower it in this commit)\n")
        for plugin, n, was in stale:
            print("   %-16s %d, baseline %d" % (plugin, n, was))
        return 1

    if not missing:
        print("css: 0 undefined in the host (%d used), %d in plugins at the baseline, "
              "0 dead JS selectors, 0 unstyled JS states" % (len(use), ptotal))
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
