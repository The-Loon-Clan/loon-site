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
    """Every class name any stylesheet writes a rule for."""
    out = set()
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


def main():
    use, have = used(), defined()
    missing = sorted(c for c in use if c not in have and c not in RUNTIME)

    if not missing:
        print("css: 0 undefined classes (%d used, all defined)" % len(use))
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
