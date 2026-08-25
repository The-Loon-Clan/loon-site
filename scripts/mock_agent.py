#!/usr/bin/env python3
"""Mock fleet agent: drive the agent UI without a real client.

    AGENT_TOKEN=secret python3 scripts/mock_agent.py
    AGENT_TOKEN=secret python3 scripts/mock_agent.py --url http://localhost:8090 --once

WHY THIS EXISTS. The host has the real agent report endpoint (POST
/api/agent/report, bearer-token gated); a real agent will download a torrent,
re-upload it to Usenet and heartbeat its progress here. This stands in for that
agent so the fleet UI -- the profile card, the admin dispatch panel, the
/admin/agents roster -- can be built and debugged before a real client exists.
It speaks the SAME wire contract, so testing the real client later reveals gaps
in the endpoint, not in a throwaway seed.

It is not connected to any real torrent or Usenet: it invents believable work
and walks each agent through downloading -> uploading -> idle on a loop, so the
page shows progress bars that actually move.
"""

import argparse
import json
import os
import random
import sys
import time
import urllib.error
import urllib.request

# A small fleet, spread across two demo members so the per-owner profile card
# has something to show for each.
AGENTS = [
    {"agent": "seedbox-01", "user": "alice"},
    {"agent": "seedbox-02", "user": "alice"},
    {"agent": "homelab", "user": "bob"},
]

# Believable release names the mock cycles through.
TITLES = [
    "The.Ark.S03E04.1080p.WEB.H264-RAWR",
    "Its.Always.Sunny.S18E01.1080p.WEB.h264-ETHEL",
    "Silo.S02E07.2160p.ATVP.WEB-DL.DDP5.1-FLUX",
    "Slow.Horses.S04E06.1080p.WEB.H264-NHTFS",
    "Shogun.S01E10.1080p.WEB.h264-ELEANOR",
]

PHASES = ["downloading", "assembling", "uploading"]


def post(url, token, body):
    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(url, data=data, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            return resp.status, resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")
    except urllib.error.URLError as e:
        return None, str(e.reason)


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

    token = os.environ.get("AGENT_TOKEN", "")
    if not token:
        sys.exit("set AGENT_TOKEN to the value the host was started with")

    report_url = args.url.rstrip("/") + "/api/agent/report"

    # Per-agent simulation state: current job, phase, progress, lifetime counts.
    state = {}
    for a in AGENTS:
        state[a["agent"]] = {
            "title": random.choice(TITLES),
            "phase_i": 0,
            "progress": random.randint(0, 30),
            "downloaded": random.randint(20, 200),
            "uploaded": random.randint(10, 150),
            "idle": False,
        }

    print("posting to", report_url, "as", len(AGENTS), "agents "
          + ("(one round)" if args.once else "(Ctrl-C to stop)"))
    while True:
        for a in AGENTS:
            st = state[a["agent"]]
            body = {"agent": a["agent"], "user": a["user"],
                    "downloaded": st["downloaded"], "uploaded": st["uploaded"]}
            if st["idle"]:
                body["phase"] = "idle"
            else:
                st["progress"] += random.randint(8, 22)
                if st["progress"] >= 100:
                    # This phase finished; advance, or complete the job.
                    st["progress"] = 0
                    st["phase_i"] += 1
                    if st["phase_i"] >= len(PHASES):
                        # Job done: bump lifetime counters, maybe idle a beat,
                        # then pick a new title.
                        st["downloaded"] += 1
                        st["uploaded"] += 1
                        st["phase_i"] = 0
                        st["title"] = random.choice(TITLES)
                        st["idle"] = random.random() < 0.3
                phase = PHASES[st["phase_i"]]
                body["phase"] = phase
                body["task_title"] = st["title"]
                body["progress"] = st["progress"]
                body["request_id"] = 1000 + (hash(st["title"]) % 9000)
                body["detail"] = _detail(phase, st["progress"])
                st["idle"] = False if body.get("phase") != "idle" else st["idle"]

            code, resp = post(report_url, token, body)
            tag = "ok" if code == 200 else "ERR %s" % code
            print("  %-12s %-11s %3s%% -> %s %s" % (
                a["agent"], body.get("phase", ""), body.get("progress", 0), tag,
                "" if code == 200 else resp[:80]))
            if code in (401, 503):
                sys.exit("endpoint refused (%s): %s" % (code, resp[:120]))
        if args.once:
            return
        time.sleep(args.interval)


def _detail(phase, pct):
    if phase == "downloading":
        total = 45
        return "%d of %d segments" % (max(1, total * pct // 100), total)
    if phase == "uploading":
        return "posting to alt.binaries.multimedia"
    return "verifying par2"


if __name__ == "__main__":
    main()
