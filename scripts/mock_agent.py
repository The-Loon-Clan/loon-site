#!/usr/bin/env python3
"""Mock fleet agent: drive the agent UI without a real client.

    AGENT_TOKEN=secret python3 scripts/mock_agent.py
    AGENT_TOKEN=secret python3 scripts/mock_agent.py --url http://localhost:8090 --once

WHY THIS EXISTS. The host runs the real agent protocol (X-Agent-Protocol v3):
a worker REGISTERS for a per-agent token, POLLs for work, POSTs a rich live
/status (VPN, public IP, transfer speeds, per-file progress) plus lightweight
/progress pings, and /completes an upload. A real agent downloads a torrent,
re-uploads it to Usenet, and speaks exactly these verbs. This stands in for
that agent so the fleet UI -- the profile card, the admin dispatch panel, the
/admin/agents roster -- can be built and debugged before a real client exists.

It speaks the SAME wire contract as loon-agent, so pointing the real client at
this host later reveals gaps in the ENDPOINT, not in a throwaway seed. It is not
connected to any real torrent or Usenet: it invents believable work -- a season
pack of several files -- and walks each agent through downloading -> assembling
-> uploading -> idle on a loop, so the page shows progress bars that move.

Bootstrap: AGENT_TOKEN is the host's MASTER token; the mock registers each agent
with it once (POST /api/agent/register) to obtain that agent's own bearer token,
then authenticates every verb with the per-agent token, never the master again.
"""

import argparse
import json
import os
import random
import sys
import time
import urllib.error
import urllib.request

PROTOCOL = "3"
VERSION = "mock-1.5.35"

# A small fleet, spread across two demo members so the per-owner profile card
# has something to show for each.
AGENTS = [
    {"agent": "seedbox-01", "user": "alice"},
    {"agent": "seedbox-02", "user": "alice"},
    {"agent": "homelab", "user": "bob"},
]

# Believable release names the mock cycles through; each is a small pack, so a
# job carries several files (exercises the per-file progress rendering).
TITLES = [
    "The.Ark.S03.1080p.WEB.H264-RAWR",
    "Its.Always.Sunny.S18.1080p.WEB.h264-ETHEL",
    "Silo.S02.2160p.ATVP.WEB-DL.DDP5.1-FLUX",
    "Slow.Horses.S04.1080p.WEB.H264-NHTFS",
    "Shogun.S01.1080p.WEB.h264-ELEANOR",
]

PHASES = ["downloading", "assembling", "uploading"]


def post(url, token, body, protocol=False):
    data = json.dumps(body).encode("utf-8") if body is not None else b""
    req = urllib.request.Request(url, data=data, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", "Bearer " + token)
    if protocol:
        # The headers prod sets on every protocol verb; the host refuses an
        # agent below the floor with 426.
        req.add_header("X-Agent-Protocol", PROTOCOL)
        req.add_header("X-Agent-Version", VERSION)
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            return resp.status, resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")
    except urllib.error.URLError as e:
        return None, str(e.reason)


def human_bytes(n):
    n = float(n)
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if n < 1024 or unit == "TB":
            return ("%d %s" % (n, unit)) if unit == "B" else ("%.1f %s" % (n, unit))
        n /= 1024


def speed_str(bytes_per_s):
    return human_bytes(bytes_per_s) + "/s"


def new_job(agent_name):
    """A fresh job: a title, a lock id, and a handful of files to move."""
    title = random.choice(TITLES)
    nfiles = random.randint(2, 4)
    files = []
    for i in range(nfiles):
        size = random.randint(700, 3500) * 1024 * 1024  # 700 MB - 3.5 GB
        files.append({
            "name": "%s.E%02d.mkv" % (title.split(".")[0], i + 1),
            "size": size,
            "transferred": 0,
        })
    return {
        "title": title,
        "lock_id": 1000 + random.randint(0, 8999),
        "phase_i": 0,
        "files": files,
        "idle": False,
    }


def build_status(agent_name, job):
    """One AgentLiveStatus snapshot, tag-for-tag with the host's contract."""
    phase = PHASES[job["phase_i"]]
    files = []
    dl_bytes = 0
    up_bytes = 0
    for f in job["files"]:
        pct = 100.0 * f["transferred"] / f["size"] if f["size"] else 0.0
        # Down speed while fetching the torrent, up speed while re-posting.
        speed = random.randint(4, 30) * 1024 * 1024 if phase == "downloading" else 0
        up = random.randint(3, 18) * 1024 * 1024 if phase == "uploading" else 0
        dl_bytes += speed
        up_bytes += up
        fp = {
            "name": f["name"],
            "size": f["size"],
            "transferred": f["transferred"],
            "percent": round(pct, 1),
            "phase": phase,
        }
        if speed:
            fp["speed"] = speed_str(speed)
        if up:
            fp["up_speed"] = speed_str(up)
        if phase == "downloading":
            fp["peers"] = random.randint(3, 40)
        files.append(fp)

    status = {
        "phase": phase,
        "vpn_status": "connected",
        "public_ip": "185.%d.%d.%d" % (random.randint(10, 99), random.randint(0, 255), random.randint(2, 254)),
        "files": files,
        "task_title": job["title"],
        "request_id": job["lock_id"],
        "disk_free_gb": round(random.uniform(120, 480), 1),
        "seeding_count": random.randint(0, 12),
    }
    if phase == "downloading" and dl_bytes:
        status["download_speed"] = speed_str(dl_bytes)
    if phase == "uploading" and up_bytes:
        status["nzb_upload_speed"] = speed_str(up_bytes)
    return status


def advance(job):
    """Move the current phase forward; return True when the whole job is done."""
    if job["phase_i"] == 0:  # downloading: fill each file
        done = True
        for f in job["files"]:
            if f["transferred"] < f["size"]:
                step = int(f["size"] * random.uniform(0.12, 0.30))
                f["transferred"] = min(f["size"], f["transferred"] + step)
            if f["transferred"] < f["size"]:
                done = False
        if done:
            job["phase_i"] = 1
    elif job["phase_i"] == 1:  # assembling: one beat
        job["phase_i"] = 2
        # Uploading re-empties the transferred bar (now it climbs on the way up).
        for f in job["files"]:
            f["transferred"] = 0
    else:  # uploading: fill each file back up, then the job completes
        done = True
        for f in job["files"]:
            if f["transferred"] < f["size"]:
                step = int(f["size"] * random.uniform(0.15, 0.35))
                f["transferred"] = min(f["size"], f["transferred"] + step)
            if f["transferred"] < f["size"]:
                done = False
        if done:
            return True
    return False


def register(base, master_token):
    """Trade the master token for one per-agent token per agent."""
    tokens = {}
    reg_url = base + "/api/agent/register"
    for a in AGENTS:
        code, resp = post(reg_url, master_token, {"agent": a["agent"], "user": a["user"]})
        if code != 200:
            sys.exit("register %s failed (%s): %s" % (a["agent"], code, resp[:160]))
        try:
            tokens[a["agent"]] = json.loads(resp)["token"]
        except (ValueError, KeyError):
            sys.exit("register %s: unexpected response: %s" % (a["agent"], resp[:160]))
    return tokens


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--url", default=os.environ.get("AGENT_HOST", "http://localhost:8090"),
                    help="host base URL (default http://localhost:8090)")
    ap.add_argument("--interval", type=float, default=2.0,
                    help="seconds between heartbeats (default 2)")
    ap.add_argument("--once", action="store_true",
                    help="post one round of heartbeats and exit (for a smoke test)")
    args = ap.parse_args()

    master = os.environ.get("AGENT_TOKEN", "")
    if not master:
        sys.exit("set AGENT_TOKEN to the host's master token (enables /api/agent/register)")

    base = args.url.rstrip("/")
    tokens = register(base, master)
    poll_url = base + "/api/agent/poll"
    status_url = base + "/api/agent/status"
    progress_url = base + "/api/agent/progress"
    complete_url = base + "/api/agent/complete"

    jobs = {a["agent"]: new_job(a["agent"]) for a in AGENTS}

    print("registered %d agents against %s %s" % (
        len(AGENTS), base, "(one round)" if args.once else "(Ctrl-C to stop)"))
    while True:
        for a in AGENTS:
            name = a["agent"]
            tok = tokens[name]
            job = jobs[name]

            # Poll for dispatched work (the demo queues none -> 204); a real
            # agent would act on a returned task. The mock invents its own.
            post(poll_url, tok, None, protocol=True)

            if job["idle"]:
                # Idle a beat, then pick up a new job next round.
                idle_status = {"phase": "idle", "vpn_status": "connected",
                               "public_ip": "185.10.0.1", "disk_free_gb": 480.0}
                code, resp = post(status_url, tok, idle_status, protocol=True)
                job["idle"] = False
                jobs[name] = new_job(name)
                print("  %-12s idle        -> %s" % (name, _tag(code, resp)))
                continue

            finished = advance(job)
            status = build_status(name, job)
            code, resp = post(status_url, tok, status, protocol=True)
            if code in (401, 426, 503):
                sys.exit("status refused (%s): %s" % (code, resp[:160]))

            # A lightweight progress ping alongside the rich status (prod sends
            # these between statuses; here they ride together to exercise both).
            overall = _overall_pct(status["files"])
            post(progress_url, tok, {
                "lock_id": job["lock_id"],
                "progress": "%.1f" % overall,
                "speed": status.get("download_speed") or status.get("nzb_upload_speed") or "",
                "warnings": "",
            }, protocol=True)

            if finished:
                post(complete_url, tok, {"lock_id": job["lock_id"],
                                         "request_id": job["lock_id"],
                                         "status": "completed"}, protocol=True)
                job["idle"] = random.random() < 0.3

            print("  %-12s %-11s %5.1f%% -> %s%s" % (
                name, status["phase"], overall, _tag(code, resp),
                "  [completed]" if finished else ""))

        if args.once:
            return
        time.sleep(args.interval)


def _overall_pct(files):
    if not files:
        return 0.0
    return sum(f["percent"] for f in files) / len(files)


def _tag(code, resp):
    return "ok" if code == 200 else ("ERR %s %s" % (code, resp[:80]))


if __name__ == "__main__":
    main()
