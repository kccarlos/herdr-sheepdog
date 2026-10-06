# CI runners on Cloud Run worker pools

GitHub-hosted Actions minutes for this private repo ran out (billing lock,
2026-10-05), so every Linux job in `.github/workflows/` runs on self-hosted
runners in Google Cloud, billed to the same billing account as the Vertex AI
project (trial credit). The iOS repo is untouched (Xcode Cloud only), and no
workflow may use macOS runners (the `policy` job enforces it).

Pattern: Google's tutorial [Host GitHub runners with Cloud Run worker
pools](https://cloud.google.com/run/docs/tutorials/github-runner) and its
sample (`GoogleCloudPlatform/cloud-run-samples/github-runner`), with the
webhook autoscaler rather than CREMA (CREMA is an always-on polling service,
~$12–17/month before any job runs).

## What exists

| Thing | Value |
| --- | --- |
| GCP project | `herdr-sheepdog-ci`, billing account "My Billing Account" (same as the Vertex project) |
| Region | `us-central1` |
| Worker pool | `gh-runners`, 4 vCPU / 8 GiB per instance, manual scaling 0..3 |
| Runner image | `us-central1-docker.pkg.dev/herdr-sheepdog-ci/ci/runner:latest` (`runner/`): `ghcr.io/actions/actions-runner` + build-essential, git, make, jq; Go 1.25.x and Node 22 pre-seeded in `/opt/hostedtoolcache`, so `actions/setup-go` / `setup-node` hit the local cache; golangci-lint v2.13 on PATH |
| Autoscaler | Cloud Run service `ci-autoscaler` (`autoscaler/`), max 1 instance, concurrency 1, scales to zero |
| Webhook | repo webhook, `workflow_job` events → autoscaler `/`, HMAC-signed |
| Safety net | Cloud Scheduler `ci-runner-reconcile`, every 10 min → `/reconcile` |
| Secrets (Secret Manager) | `github-runner-token` (GitHub token), `github-webhook-secret`; readable only by `ci-autoscaler` |
| Service accounts | `ci-runner` (pool: log writer, image reader, nothing else), `ci-autoscaler` (secrets above, `run.developer` on the pool only, act-as `ci-runner`), `ci-builder` (Cloud Build: image push, logs) |
| Budget | $20/month on the project, e-mail at 50 / 90 / 100 % |
| Runner labels | `self-hosted, linux, x64, cloudrun`; workflows use `runs-on: ${{ vars.CI_RUNNER \|\| 'cloudrun' }}` |

## How it works

1. A job is queued → GitHub sends `workflow_job` → the autoscaler counts the
   queued and running jobs that want `cloudrun` and raises the pool's
   `manualInstanceCount` to that number (capped at `MAX_RUNNERS`, 3).
2. Each instance runs `runner/start.sh` in a loop: it gets a Google ID token
   from the metadata server, `POST /jit` to the autoscaler, which checks the
   token is `ci-runner`'s and returns a just-in-time runner config from
   GitHub. The runner takes exactly one job, exits, wipes its work directory,
   and registers again (ephemeral).
3. When a job completes and nothing is queued or running, the pool goes to
   0 and offline `cloudrun-*` runners are deleted. The pool is never scaled
   *down* while a job is active, so no running job is killed. The scheduler
   repairs missed webhooks in both directions.

A cold start (0 → N instances, image import) takes about 1–2 minutes before
the first job starts.

## Security notes

- The runners never see the GitHub token: only the autoscaler holds it. A
  job can at most ask the autoscaler for another JIT registration.
- Runners exist only for this repository (repo-level registration).
- Fork pull requests: the repo is private and "Run workflows from fork pull
  requests" is off (`run_workflows_from_fork_pull_requests: false`), so
  workflows from forks never reach these runners. Keep it off.
- Anyone who can push a branch here can run code on the runner (as with any
  CI), including reading the runner's metadata-server identity, which can
  write logs and pull the image, nothing more.
- The stored token is currently the owner's `gh` CLI OAuth token (`repo`
  scope on all their repos). Replace it with a fine-grained PAT (see
  "Rotate the token"). If the owner runs `gh auth logout`/`login`, that token
  may be revoked: symptom is jobs stuck "Waiting for a runner" and
  `generate-jitconfig failed: 401` in the autoscaler log.

## Operating it

All commands from the repo root; they need `gcloud` logged in as the owner
and `gh`. `gcloud run worker-pools` needs the Python module `grpc`; with
Homebrew's gcloud create once `python3 -m venv ~/.venvs/gcloud &&
~/.venvs/gcloud/bin/pip install grpcio` (the script uses it automatically).

```bash
infra/ci-runners/ci-runners.sh status          # pool size, runners, autoscaler log
infra/ci-runners/ci-runners.sh scale 0         # stop all runners now
infra/ci-runners/ci-runners.sh build && infra/ci-runners/ci-runners.sh deploy   # new image / config
```

From scratch: `setup`, `secrets`, `build`, `deploy`, `webhook`, `scheduler`,
`budget`, in that order.

**Update the runner monthly.** GitHub stops sending jobs to runner versions
too far behind the latest release. `build` always uses the newest
`actions/runner` release; run `build` and `deploy` when a new one ships or
when bumping Go (go.mod), Node or golangci-lint (ARGs in `runner/Dockerfile`;
setup-go falls back to downloading if the cached version does not match, so
a stale image is slower, not broken).

**Rotate the token.** Create a fine-grained PAT (github.com → Settings →
Developer settings → Fine-grained tokens → Generate): resource owner
`kccarlos`, only `kccarlos/herdr-sheepdog`, permissions *Administration:
Read and write* (runner registration) and *Actions: Read* (queue counts).
Save it to a file outside the repo, then:

```bash
GITHUB_PAT_FILE=~/path/to/pat infra/ci-runners/ci-runners.sh rotate-token && rm ~/path/to/pat
```

**Scale.** `MAX_RUNNERS` (default 3) and `RUNNER_CPU`/`RUNNER_MEMORY`
(default 4 / 8Gi; the first full run peaked near 20 % memory and 70 % CPU)
are env vars of `deploy`, e.g. `MAX_RUNNERS=2 infra/ci-runners/ci-runners.sh deploy`.

**Switch back to GitHub-hosted runners** (once Actions billing works again):

```bash
gh variable set CI_RUNNER --body ubuntu-latest   # all workflows, immediately
gh variable delete CI_RUNNER                     # back to Cloud Run
```

**Tear down.** `infra/ci-runners/ci-runners.sh teardown` removes webhook,
scheduler, service and pool; `gcloud projects delete herdr-sheepdog-ci`
removes everything else. Switch `CI_RUNNER` to `ubuntu-latest` first.

## Cost

Worker pools bill per instance-second while an instance exists
(us-central1: $0.000011244 per vCPU-s, $0.000001235 per GiB-s), so a
4 vCPU / 8 GiB runner costs about **$0.0033 per minute** ($0.20/hour). The
free tier covers the first 384,204 vCPU-s and 728,744 GiB-s each month
(about 26 instance-hours of this size, ~$5).

One CI run (7 jobs; `test` with `-race` takes ~5 min, the rest under 2 min)
keeps up to 3 instances for ~7 minutes including cold start: about
20 instance-minutes, **~$0.07 per run**. At 200–400 runs a month that is
$14–28 before the free tier, **roughly $10–25/month** after it. The
autoscaler, scheduler (3 jobs free per billing account), Artifact Registry
(two images, cleanup keeps 3 versions) and Secret Manager stay within free
tiers or cents. Idle cost is zero. Watch the $20 budget alert; the cheapest
knobs are `MAX_RUNNERS=2` and fewer pushes per PR.
