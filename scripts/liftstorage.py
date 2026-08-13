#!/usr/bin/env python3
"""Lift data-access declarations out of web/handlers into internal/storage.

    python scripts/liftstorage.py <domain> <sym>[,<sym>...]

    python scripts/liftstorage.py follows readFollows,followCounts,isFollowing

Writes internal/storage/<domain>.go, removes the declarations from wherever
they lived, exports their names, rewrites every call site, and runs goimports
over the lot.

Why a script
------------
94 SQL-bearing functions are still filed beside the handlers that call them.
Each move is the same five mechanical steps, and doing them by hand is how the
same three mistakes keep recurring: an identifier that appears in a COMMENT
treated as a use, a function passed as a VALUE (`Field: fn`) missed by a
`fn(`-shaped pattern, and a rename that also rewrites a template funcmap KEY
because the key happens to spell the same word. The first two are handled
below; the third is why renames here are confined to Go identifiers and the
caller rewrite never touches string literals.

It is deliberately not clever. It does not work out which types or helpers a
lifted function needs — the compiler does that, by failing, and the missing
name goes on the next run's list. A tool that guessed would guess wrong
silently, and this whole exercise is an argument against that.
"""
import re
import subprocess
import sys
from pathlib import Path

HANDLERS = Path("web/handlers")
STORAGE = Path("internal/storage")
MODULE = "github.com/the-loon-clan/loon-demo-site"


def write(path, text):
    """Write with LF endings.

    Path.write_text grew a newline= parameter in 3.10; this runs on 3.9, where
    it silently is not available and raises instead. Going through open() keeps
    the endings pinned regardless of interpreter or platform.
    """
    with open(path, "w", encoding="utf-8", newline="\n") as f:
        f.write(text)


def strip_comments(src: str) -> str:
    """Source with comments removed — for deciding whether a name is USED.

    A name mentioned in prose is not a reference to it. Skipping this step has
    produced an unused import three separate times in this refactor.
    """
    src = re.sub(r'/\*.*?\*/', '', src, flags=re.S)
    return re.sub(r'(?m)//.*$', '', src)


def find_decl(src: str, name: str):
    """(start, end) of a top-level declaration and its leading comment block."""
    pat = re.compile(
        r'(?m)^((?:(?://[^\n]*\n)+)?)(?:func|type|var|const)\s+%s\b' % re.escape(name))
    m = pat.search(src)
    if not m:
        # The name may be one entry inside a grouped `const (` / `var (` block,
        # where it does not begin a declaration of its own. Lift the WHOLE
        # block: the entries in one group are a set — gift minimum, maximum and
        # note length are meaningless apart — and splitting them would leave
        # half the group behind for the compiler to complain about next run.
        for gm in re.finditer(r'(?m)^((?:(?://[^\n]*\n)+)?)(?:const|var) \(\n', src):
            end = src.find('\n)\n', gm.end())
            if end == -1:
                continue
            block = src[gm.end():end]
            if re.search(r'(?m)^\t%s\b' % re.escape(name), block):
                return gm.start(), end + len('\n)\n')
        return None
    start = m.start()
    # Where a declaration ENDS is decided by how its first line finishes, not
    # by hunting for the next closing token. Hunting is what the first version
    # did, and on `type followKind string` — a one-liner — it ran past the
    # const block below it and swallowed a whole handler method into
    # internal/storage, which then failed to compile against an undefined
    # `web` type. The compiler caught it; a subtler over-capture might not.
    line_end = src.find('\n', m.end())
    if line_end == -1:
        return start, len(src)
    first_line = src[m.start():line_end].rstrip()
    closer = {'{': '\n}\n', '(': '\n)\n'}.get(first_line[-1:] if first_line else '')
    if closer is None:
        return start, line_end + 1          # one-liner: type X string, var x = 1
    i = src.find(closer, line_end)
    return start, (i + len(closer)) if i != -1 else len(src)


def go_files(*dirs):
    out = []
    for d in dirs:
        out += sorted(Path(d).glob("*.go"))
    return out


def main():
    if len(sys.argv) < 3:
        print(__doc__)
        return 2
    domain, syms = sys.argv[1], [s.strip() for s in sys.argv[2].split(",") if s.strip()]
    exported = {s: s[0].upper() + s[1:] for s in syms}

    lifted, imports = [], set()
    for path in go_files(HANDLERS):
        if path.name.endswith("_test.go"):
            continue
        src = path.read_text(encoding="utf-8").replace("\r\n", "\n")
        spans = []
        for s in syms:
            found = find_decl(src, s)
            if found:
                lifted.append(src[found[0]:found[1]])
                spans.append(found)
        if not spans:
            continue
        # Carry the file's imports over; goimports drops the unused ones.
        blk = re.search(r'(?m)^import \(\n(.*?)\n\)\n', src, re.S)
        if blk:
            imports |= {l.strip() for l in blk.group(1).split("\n") if l.strip()}
        for a, b in sorted(spans, reverse=True):
            src = src[:a] + src[b:]
        write(path, src)

    # Only rewrite what actually MOVED.
    #
    # Rewriting a symbol that failed to lift does not merely miss — it renames
    # the declaration that stayed behind. `giftMin = 1` inside a const block
    # became `storage.GiftMin = 1`, which is not valid Go, and the same pass
    # had already renamed every use, so the error surfaced in the new file
    # rather than the damaged one.
    found = set()
    for blk in lifted:
        found |= set(re.findall(r'(?m)^(?:func|type|var|const)\s+(\w+)', blk))
        found |= set(re.findall(r'(?m)^\t(\w+)\s', blk))   # entries in a grouped block
    missing = [s for s in syms if s not in found]
    if missing:
        print("NOT LIFTED (skipped, not rewritten):", ", ".join(missing))
    exported = {s: n for s, n in exported.items() if s in found}
    if not lifted:
        return 1

    body = "\n".join(lifted)
    for old, new in exported.items():
        body = re.sub(r'\b%s\b' % old, new, body)

    out = STORAGE / f"{domain}.go"
    header = "" if out.exists() else "package storage\n\nimport (\n%s\n)\n\n" % "\n".join(
        "\t" + i for i in sorted(imports))
    if out.exists():
        prev = out.read_text(encoding="utf-8").replace("\r\n", "\n")
        write(out, prev.rstrip() + "\n\n" + body)
    else:
        write(out, header + body)

    # Rewrite call sites. Word-boundary on the bare identifier, so a function
    # passed as a VALUE is caught as well as one being called.
    touched = 0
    for path in go_files(HANDLERS, STORAGE, "internal/middleware", "internal/markdown"):
        src = path.read_text(encoding="utf-8").replace("\r\n", "\n")
        orig = src
        for old, new in exported.items():
            repl = new if path.parent == STORAGE else "storage." + new
            src = re.sub(r'(?<![.\w])%s\b' % old, repl, src)
        if src != orig:
            write(path, src)
            touched += 1

    subprocess.run(["gofmt", "-w", str(STORAGE), str(HANDLERS)], capture_output=True)
    gi = Path.home() / "go" / "bin" / "goimports.exe"
    if gi.exists():
        subprocess.run([str(gi), "-w", str(STORAGE), str(HANDLERS)], capture_output=True)

    print(f"{domain}: lifted {len(lifted)} decls, rewrote {touched} files -> {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
