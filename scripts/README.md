# Audit scripts

Three sweeps that check the things nothing else does — the failures that return
200, render, and look fine.

```sh
python scripts/audit_css.py     # static: no running site needed
python scripts/audit_links.py   # needs the site up
python scripts/audit_a11y.py    # needs the site up
```

`deploy.sh` runs all three after a successful deploy and prints what they find.
It does **not** fail the deploy on findings — see *Advisory, not blocking* below.

Standard library only. A tool that needs `pip install` before it can tell you
the site is broken is a tool nobody runs.

## Why these exist

Every one of them looks for a failure that **produces no error**:

| Script | Finds | Why nothing else catches it |
|---|---|---|
| `audit_css.py` | classes used in a template, defined in no stylesheet | A missing class has no effect and raises nothing. The element renders as though the class were absent, which is indistinguishable from being styled to look plain. |
| `audit_links.py` | 404s, 5xx, and **200s whose HTML stops before `</footer>`** | A template that fails at execute time aborts mid-document and still returns 200. The page just looks short. |
| `audit_a11y.py` | unnamed controls, missing/duplicate `h1`, heading skips, unlabelled navs, missing `aria-current` | All of it renders perfectly for a sighted mouse user. |

They have earned their place:

* `audit_css.py` found `.button--primary` used on the main action of **fourteen**
  forms and defined nowhere — every Save, Upload and Create rendering identical
  to the Cancel beside it. It was written after `.button--danger` and
  `.text-danger` turned out to have been in that state for the life of the
  codebase.
* `audit_links.py` found `/sitemap` 404ing behind two nav links, `/p/topics`
  and `/p/posts` linking `threadS/<id>`, `/u//followers` with an empty
  username, a 500 on `/c/usenet`, and a broken link written an hour earlier.
* `audit_a11y.py` found 2 pages with no `h1`, nav strips marking the current
  item to sighted readers only, and unnamed tables.

## Advisory, not blocking

Same reasoning as the contract audit at boot (`contracts_web.go`): a check that
stops the build is a check people learn to skip. Each script exits non-zero when
it finds something, so **CI can gate on any one of them once its findings are at
zero** — but `deploy.sh` only reports.

`audit_css.py` is the closest to gateable: its remaining findings are structural
BEM names in plugin markup rather than typos.

## Known false-positive shapes

Both are handled, and both are recorded because the fix is not obvious:

* **Template actions containing quotes.** `class="x{{if eq .Path "/y"}}…"`
  breaks a naive `class="([^"]*)"` match mid-action, which reported `.hasPrefix`,
  `.eq` and `.or` — template functions, not classes. Actions are blanked before
  attributes are matched.
* **Classes built at render time.** `poster--h{{hue .Name}}` is one token, not a
  class called `poster--h`. Any token still carrying the marker is skipped.
* **`/admin/jobs/config` reads as a 404** in the link crawler. It is not: the
  crawler strips query strings and the real link carries `?name=`. Recorded here
  so nobody chases it a third time.

## Adding an exception

`audit_css.py` has a `RUNTIME` set for classes applied by JavaScript rather than
written in a template. **Every entry carries a reason.** An ignore list without
them becomes the place findings go to die.
