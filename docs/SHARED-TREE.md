# The shared working tree

Two agents work in one checkout of `loon`, `loon-plugins` and `loon-baseline`.
This records what that costs, what has been done about it, and the one decision
left — which is yours, because it is about how you want to work rather than
about the code.

## The failure, precisely

The demo site is built from **sibling working trees**, not from commits:

```yaml
additional_contexts:
  loon: ../loon
  loonplugins: ../loon-plugins
  loonbaseline: ../loon-baseline
```

So `docker compose build` bakes in whatever is on disk — staged, unstaged and
untracked alike. That is convenient and it is why the outages happen.

Three in one day, all the same shape: a change needing both `loon/core` and a
plugin, one half committed and the other sitting uncommitted. Once, an
uncommitted fix was discarded by the other agent's recovery (`git checkout .`
or equivalent) and the demo site stopped compiling — the seam it depended on
was simply gone, with nothing in either repository's history to show it had
ever existed.

The uncomfortable part is that **nobody did anything wrong**. Recovering a dirty
tree is a normal thing to do. It is only destructive because a second party had
work in it.

## What is already in place

* **Commit promptly.** Adopted after the first loss. It shrinks the window; it
  does not close it.
* **`deploy.sh` names the symptom.** It waits for `/healthz` and shows boot
  errors, so a half-wired build fails visibly at deploy rather than mysteriously
  an hour later.
* **`deploy.sh` now names the *cause*.** Before each successful deploy it lists
  any uncommitted files in the three shared checkouts, because those are exactly
  what just went into the image:

  ```
    shared checkouts with uncommitted work (baked into this image):
       loon-plugins
          ?? events/
          ?? pluginapi/schedevents.go
       ^ commit these where they belong, or a recovery in that tree loses them.
  ```

  Advisory, never blocking. Uncommitted work is normal while developing; the
  value is knowing *which* uncommitted files are in the image you are about to
  run, so "it worked on my deploy" has an explanation and a recovery in the
  other tree is recognisable as the thing that broke it.

That is as far as code can take it. The remaining fix is a workflow choice.

## The options

### 1. Leave it, with the guard

Nothing changes except that the risk is now visible at deploy time.

*For:* zero friction; both agents see the whole tree, which is genuinely useful
for cross-repo changes. *Against:* the loss mode is still there. The guard tells
you what was at risk, not what was destroyed — a recovery that already happened
leaves no trace.

### 2. A worktree per agent

`git worktree add ../loon-agent2 <branch>` for each shared repo, and point the
second agent's compose file at those paths.

*For:* removes the cause outright. Each agent has its own files; a recovery in
one cannot touch the other. Branches and history stay shared, so nothing is
forked. *Against:* three extra directories per agent, a second compose overlay
for the paths, and cross-repo changes need a deliberate merge instead of just
existing on disk.

### 3. Split by ownership

One agent owns `loon` + `loon-baseline`, the other owns `loon-plugins`; anything
crossing the line goes through a commit and a request.

*For:* no tooling at all. *Against:* the outages were **caused** by cross-repo
changes, which is exactly what this makes slowest. It optimises against the
common case.

## Recommendation

**Option 2**, and only for the repos that have actually bitten — `loon` and
`loon-plugins`. `loon-baseline` has not been the site of a single incident.

Option 1 keeps a known data-loss path open for the sake of convenience the
guard already mostly recovers. Option 3 taxes the exact change the trouble came
from.

It is a small change and it is not mine to make: it decides how you and the
other agent work, and either of you can undo it in a minute.
