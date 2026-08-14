# Upgrading

What this project promises across versions, and what it does not.

## The short version

```sh
docker compose pull && docker compose up -d
```

Migrations run themselves at boot. There is no separate migration step, and
there is no window in which the schema and the binary disagree — the binary
migrates before it serves.

## What you are running

Every build says so, on its first line, before anything that can fail:

```
msg="loon-site starting" version="v0.1.0 (a1b2c3d)"
```

and from outside the container:

```sh
curl -s localhost:8090/healthz
# ok v0.1.0 (a1b2c3d)
```

`dev` means a binary built from a working tree rather than a release. A
published image never says `dev`; if one does, that is a bug in the release
workflow and worth reporting. `modified` in the same string means the tree had
uncommitted changes when it was built, so no commit matches what is running.

## The guarantee

**Migrations only go forwards, and they are applied automatically.** Each
plugin migration is applied once and recorded in `core.plugin_migrations`
(`owner`, `filename`, `applied_at`). The host's own tables are created with
idempotent `CREATE TABLE IF NOT EXISTS` statements that re-run harmlessly every
boot. Starting a newer binary against an older database is the supported path
and needs nothing from you.

**There is no downgrade path.** Starting an OLDER binary against a database a
newer one has migrated is not supported and is not detected — the old binary
will run, and will read a schema it was not written for. If you need to roll
back, restore the database from before the upgrade. Take a backup first:

```sh
docker compose exec -T db pg_dump -U demo loon_demo | gzip > loon-$(date +%F).sql.gz
```

**Your data is in named volumes**, which survive `docker compose down`. Only
`down -v` removes them, and that is a flag you have to mean. The volume names
are pinned in `docker-compose.yml` rather than derived from the directory name,
so renaming your checkout does not orphan the database.

**Plugins migrate their own schemas**, each into its own Postgres schema
(`tracker.*`, `guestbook.*`, …). Enabling a plugin later is a config change and
a restart: the migrations ran at first boot whether the plugin was switched on
or not, so there is no first-run schema surprise the day you turn it on.

## Before you upgrade

Read the [CHANGELOG](../CHANGELOG.md) section for the version you are moving
to. Anything that changes the behaviour of a running site is called out there,
and it is where a fix is described in terms of what it does to you rather than
what it did to the code.

One change so far needs an action rather than a read:

- **`LOON_TRUSTED_PROXIES`** — the site no longer believes `X-Forwarded-For`
  from anybody by default. If you run behind nginx, Cloudflare or any other
  proxy and do not set this, every request will be recorded as coming from the
  proxy's own address. That affects the login history members read to spot a
  compromised account. See the README's load-balancer section for why trusting
  one address is right and trusting the whole subnet is not.

## Running jobs separately

If you have split the roles (`LOON_ROLE`), upgrade both containers together.
They share a database and a job-trigger queue, and nothing enforces that they
are the same version — a mixed pair will usually work and is not a state
anybody has tested.

```sh
LOON_ROLE=web docker compose -f docker-compose.yml -f compose.worker.yml pull
LOON_ROLE=web docker compose -f docker-compose.yml -f compose.worker.yml up -d
```

## If an upgrade goes wrong

Ask the database what ran, and when. Nothing is written to the log about
migrations — this was checked while writing this page, and the obvious advice
("grep the boot log") would have sent you looking for lines that do not exist:

```sh
docker compose exec -T db psql -U demo -d loon_demo -c \
  "select owner, filename, applied_at from core.plugin_migrations order by applied_at desc limit 20;"
```

```
 owner  |          filename           |          applied_at
--------+-----------------------------+-------------------------------
 usenet | 036_articles_obfuscated.sql | 2026-08-13 05:24:21.050001+00
```

Anything applied during the upgrade carries a timestamp from the upgrade. The
host's own tables do not appear there — they are idempotent statements rather
than recorded migrations, so there is nothing to list and nothing that can be
half-applied.

If it does not come up at all, the version line is still the first thing
printed, so a failing container reports what it is before it reports why.
