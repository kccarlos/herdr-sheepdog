"""Autoscaler and JIT-config broker for the Cloud Run worker pool of GitHub runners.

Adapted from Google's sample (cloud-run-samples/github-runner/autoscaler).
Changes: the instance count is computed from GitHub's actual queue (not +1
per event, which races when a CI run queues seven jobs at once), the pool is
only ever scaled *down* to zero (so a busy runner is never killed), runners get
just-in-time configs from here so they never hold the GitHub token, and a
scheduled /reconcile call repairs missed webhooks.

Routes (one Cloud Run service, public; every route authenticates itself):
  POST /           GitHub `workflow_job` webhook, HMAC-SHA256 checked.
  POST /jit        Runner asks for a JIT config; Google ID token of RUNNER_SA.
  POST /reconcile  Cloud Scheduler; Google ID token of SCHEDULER_SA.
"""

import hashlib
import hmac
import logging
import os

import google.auth
import requests
from flask import Flask, abort, jsonify, request
from google.auth.transport.requests import AuthorizedSession
from google.auth.transport.requests import Request as GoogleRequest
from google.oauth2 import id_token

logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s")
log = logging.getLogger("autoscaler")

GITHUB_REPO = os.environ["GITHUB_REPO"]  # owner/name
GITHUB_TOKEN = os.environ["GITHUB_TOKEN"].strip()
WEBHOOK_SECRET = os.environ["WEBHOOK_SECRET"].strip()
POOL = os.environ["WORKER_POOL"]  # projects/P/locations/R/workerPools/N
RUNNER_SA = os.environ["RUNNER_SA"]
SCHEDULER_SA = os.environ["SCHEDULER_SA"]
AUDIENCE = os.getenv("AUDIENCE", "")  # this service's URL, set after the first deploy
MAX_RUNNERS = int(os.getenv("MAX_RUNNERS", "4"))
LABEL = os.getenv("RUNNER_LABEL", "cloudrun")
RUNNER_PREFIX = "cloudrun-"

GH = "https://api.github.com/repos/" + GITHUB_REPO
RUN_API = "https://run.googleapis.com/v2/" + POOL

_creds, _ = google.auth.default(scopes=["https://www.googleapis.com/auth/cloud-platform"])
gcp = AuthorizedSession(_creds)
gh = requests.Session()
gh.headers.update(
    {
        "Authorization": "Bearer " + GITHUB_TOKEN,
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
    }
)

app = Flask(__name__)


# --- GitHub -----------------------------------------------------------------

def active_jobs():
    """Number of queued or running jobs that want a `LABEL` runner."""
    n = 0
    for status in ("queued", "in_progress"):
        r = gh.get(GH + "/actions/runs", params={"status": status, "per_page": 50}, timeout=20)
        r.raise_for_status()
        for run in r.json()["workflow_runs"]:
            jr = gh.get(run["jobs_url"], params={"per_page": 100}, timeout=20)
            jr.raise_for_status()
            for job in jr.json()["jobs"]:
                if job["status"] in ("queued", "in_progress", "waiting") and LABEL in job.get("labels", []):
                    n += 1
    return n


def delete_offline_runners():
    r = gh.get(GH + "/actions/runners", params={"per_page": 100}, timeout=20)
    r.raise_for_status()
    for rn in r.json()["runners"]:
        if rn["name"].startswith(RUNNER_PREFIX) and rn["status"] == "offline" and not rn["busy"]:
            gh.delete(f"{GH}/actions/runners/{rn['id']}", timeout=20)
            log.info("deleted offline runner %s", rn["name"])


# --- Cloud Run --------------------------------------------------------------

def pool_count():
    r = gcp.get(RUN_API, timeout=20)
    r.raise_for_status()
    return int(r.json().get("scaling", {}).get("manualInstanceCount", 0))


def set_pool_count(n):
    r = gcp.patch(
        RUN_API,
        params={"updateMask": "scaling.manualInstanceCount"},
        json={"scaling": {"scalingMode": "MANUAL", "manualInstanceCount": n}},
        timeout=30,
    )
    if r.status_code >= 300:
        log.error("scale to %d failed: %s %s", n, r.status_code, r.text[:500])
        r.raise_for_status()
    log.info("worker pool scaled to %d", n)


def reconcile(reason):
    """Scale up to the queue (capped); scale to zero only when nothing is active."""
    active = active_jobs()
    current = pool_count()
    want = min(MAX_RUNNERS, active)
    log.info("reconcile(%s): active=%d current=%d want=%d", reason, active, current, want)
    if want > current:
        set_pool_count(want)
    elif active == 0 and current > 0:
        set_pool_count(0)
        delete_offline_runners()
    elif active == 0 and current == 0:
        delete_offline_runners()
    return {"active": active, "current": current, "want": want}


# --- Auth helpers -----------------------------------------------------------

def require_google_caller(email):
    auth = request.headers.get("Authorization", "")
    if not AUDIENCE or not auth.startswith("Bearer "):
        abort(401)
    try:
        claims = id_token.verify_oauth2_token(auth[7:], GoogleRequest(), audience=AUDIENCE)
    except ValueError:
        abort(401)
    if claims.get("email") != email or not claims.get("email_verified"):
        abort(403)


def valid_signature():
    sig = request.headers.get("X-Hub-Signature-256", "")
    mac = hmac.new(WEBHOOK_SECRET.encode(), msg=request.get_data(), digestmod=hashlib.sha256)
    return hmac.compare_digest("sha256=" + mac.hexdigest(), sig)


# --- Routes -----------------------------------------------------------------

@app.post("/")
def webhook():
    if not valid_signature():
        return ("invalid signature", 403)
    event = request.headers.get("X-GitHub-Event", "")
    if event == "ping":
        return ("pong", 200)
    if event != "workflow_job":
        return ("ignored", 200)
    payload = request.get_json(silent=True) or {}
    job = payload.get("workflow_job") or {}
    if LABEL not in job.get("labels", []):
        return ("not ours", 200)
    log.info("workflow_job %s: %s (%s)", payload.get("action"), job.get("name"), job.get("id"))
    if payload.get("action") not in ("queued", "completed"):
        return ("ok", 200)
    return jsonify(reconcile("webhook " + payload.get("action", "")))


@app.post("/jit")
def jit():
    require_google_caller(RUNNER_SA)
    name = (request.get_json(silent=True) or {}).get("name", "")
    if not name.startswith(RUNNER_PREFIX) or len(name) > 64:
        return ("bad name", 400)
    r = gh.post(
        GH + "/actions/runners/generate-jitconfig",
        json={
            "name": name,
            "runner_group_id": 1,
            "labels": ["self-hosted", "linux", "x64", LABEL],
            "work_folder": "_work",
        },
        timeout=20,
    )
    if r.status_code >= 300:
        log.error("generate-jitconfig failed: %s %s", r.status_code, r.text[:300])
        return ("github error", 502)
    return (r.json()["encoded_jit_config"], 200, {"Content-Type": "text/plain"})


@app.post("/reconcile")
def reconcile_route():
    require_google_caller(SCHEDULER_SA)
    return jsonify(reconcile("scheduler"))


@app.get("/health")
def health():
    return "ok"


if __name__ == "__main__":
    app.run(host="0.0.0.0", port=int(os.getenv("PORT", "8080")))
