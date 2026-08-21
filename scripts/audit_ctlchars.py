#!/usr/bin/env python3
"""Refuse stray control characters in source.

    python scripts/audit_ctlchars.py [dir ...]

Why this exists
---------------
sqllint's fmt.Sprintf rule carried a literal backspace (0x08) inside its regex:

    r'[`"]\\s*(SELECT|INSERT|UPDATE|DELETE|CREATE|ALTER|DROP)<BS>'

Somebody typed \\b -- a word boundary -- and something between the keyboard and
the file wrote the character \\b DENOTES. The pattern then required an actual
backspace after the SQL verb, so it could never match. The rule was dead for
its entire life, and a clean run over the repository was read as proof the
linter worked.

The same accident had disabled audit_links.py's tokenless-form check:

    r'<form<BS>[^>]*<BS>method=.?post[^>]*>(.*?)</form>'

That one had never flagged anything either. When it was fixed it immediately
found eight POST forms with no CSRF token, every one of which answers 403 to
every human who clicks it.

Two security checks, silently dead, for the same one-byte reason. Nothing
showed it: an editor renders 0x08 as nothing, a diff shows an unchanged line,
and review sees a correct-looking regex.

What counts
-----------
Tab, newline and carriage return are legitimate. Every other C0 control
character in a source file is a paste accident, and this fails the build on
one.
"""
import io
import os
import sys

ALLOWED = {0x09, 0x0a, 0x0d}  # tab, LF, CR
EXTS = ('.py', '.go', '.sh', '.yml', '.yaml', '.html', '.js', '.css', '.md', '.sql')
SKIP_DIRS = {'.git', 'node_modules', '__pycache__', 'clonecheck', 'vendor', '.venv'}


def offenders(root):
    """Every (path, line, byte) this tree should not contain."""
    out = []
    for base, dirs, names in os.walk(root):
        dirs[:] = [d for d in dirs if d not in SKIP_DIRS]
        for n in names:
            if not (n.endswith(EXTS) or n == 'Makefile'):
                continue
            p = os.path.join(base, n)
            try:
                data = io.open(p, 'rb').read()
            except OSError:
                continue
            for i, b in enumerate(bytearray(data)):
                if b < 0x20 and b not in ALLOWED:
                    rel = os.path.relpath(p, root).replace(os.sep, '/')
                    out.append((rel, data[:i].count(b'\n') + 1, b))
                    break  # one report per file is enough to go and look
    return out


def main():
    roots = sys.argv[1:] or ['.']
    found = []
    files = 0
    for r in roots:
        if not os.path.isdir(r):
            print('audit_ctlchars: no such directory: %s' % r)
            return 1
        for rel, line, b in offenders(r):
            found.append((r, rel, line, b))
        files += 1

    if not found:
        print('ctlchars: %d tree(s), no stray control characters' % files)
        return 0

    print('ctlchars: %d file(s) with a control character that should not be there\n' % len(found))
    for root, rel, line, b in found:
        print('  %s/%s:%d  0x%02x' % (root.rstrip('/.'), rel, line, b))
    print('\nA 0x08 here is almost always a \\b that was meant to be two')
    print('characters. It is invisible in an editor and in a diff, and it')
    print('silently disabled two security checks in this project.')
    return 1


if __name__ == '__main__':
    sys.exit(main())
