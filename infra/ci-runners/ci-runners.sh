#!/usr/bin/env bash
# Self-hosted GitHub Actions runners on Cloud Run worker pools.
# Usage: infra/ci-runners/ci-runners.sh <command>   (see README.md)
#
#   setup            project, billing link, APIs, Artifact Registry, service accounts, IAM
#   secrets          store the GitHub token (from `gh auth token`, or GITHUB_PAT_FILE) and a webhook secret
#   rotate-token     replace the GitHub token secret (reads GITHUB_PAT_FILE or `gh auth token`), restart autoscaler
#   build            build both images with Cloud Build
#   deploy           deploy autoscaler service + worker pool (pool keeps its current count, 0 on first deploy)
#   webhook          create the repo's workflow_job webhook pointing at the autoscaler
#   scheduler        Cloud Scheduler job calling /reconcile every 10 minutes
#   budget           $BUDGET USD monthly budget alert on the project
#   scale N          set the worker pool instance count by hand
#   status           pool count, registered runners, recent autoscaler logs
#   teardown         delete webhook, scheduler, service, pool (project kept)
set -euo pipefail

PROJECT=${PROJECT:-herdr-sheepdog-ci}
REGION=${REGION:-us-central1}
BILLING_ACCOUNT=${BILLING_ACCOUNT:-01418B-3B7F00-64DA3D}
REPO=${REPO:-kccarlos/herdr-sheepdog}
POOL=${POOL:-gh-runners}
SERVICE=${SERVICE:-ci-autoscaler}
MAX_RUNNERS=${MAX_RUNNERS:-3}
RUNNER_CPU=${RUNNER_CPU:-4}
RUNNER_MEMORY=${RUNNER_MEMORY:-8Gi}
BUDGET=${BUDGET:-20}
AR=${REGION}-docker.pkg.dev/${PROJECT}/ci
RUNNER_SA=ci-runner@${PROJECT}.iam.gserviceaccount.com
SCALER_SA=ci-autoscaler@${PROJECT}.iam.gserviceaccount.com
BUILD_SA=ci-builder@${PROJECT}.iam.gserviceaccount.com
HERE=$(cd "$(dirname "$0")" && pwd)

# gcloud's `run worker-pools` commands import grpc. Homebrew's gcloud runs on a
# Python without it; point CLOUDSDK_PYTHON at a venv that has grpcio
# (python3 -m venv ~/.venvs/gcloud && ~/.venvs/gcloud/bin/pip install grpcio).
if [ -z "${CLOUDSDK_PYTHON:-}" ] && [ -x "$HOME/.venvs/gcloud/bin/python" ]; then
  export CLOUDSDK_PYTHON=$HOME/.venvs/gcloud/bin/python CLOUDSDK_PYTHON_SITEPACKAGES=1
fi
g() { gcloud --project "$PROJECT" "$@"; }

proj_role() { g projects add-iam-policy-binding "$PROJECT" --member="serviceAccount:$1" --role="$2" --condition=None -q >/dev/null; }

setup() {
  gcloud projects describe "$PROJECT" >/dev/null 2>&1 || gcloud projects create "$PROJECT" --name="herdr-sheepdog CI"
  gcloud billing projects link "$PROJECT" --billing-account="$BILLING_ACCOUNT"
  g services enable run.googleapis.com artifactregistry.googleapis.com cloudbuild.googleapis.com \
    secretmanager.googleapis.com cloudscheduler.googleapis.com billingbudgets.googleapis.com \
    iam.googleapis.com logging.googleapis.com
  g artifacts repositories describe ci --location="$REGION" >/dev/null 2>&1 ||
    g artifacts repositories create ci --repository-format=docker --location="$REGION" --description="CI runner images"
  # Keep the 3 newest images of each package, delete the rest.
  cat >"${TMPDIR:-/tmp}/ar-cleanup.json" <<'EOF'
[{"name":"keep-recent","action":{"type":"Keep"},"mostRecentVersions":{"keepCount":3}},
 {"name":"delete-old","action":{"type":"Delete"},"condition":{"olderThan":"1d"}}]
EOF
  g artifacts repositories set-cleanup-policies ci --location="$REGION" --policy="${TMPDIR:-/tmp}/ar-cleanup.json" --no-dry-run -q >/dev/null
  for sa in ci-runner:"GitHub runner (worker pool)" ci-autoscaler:"GitHub runner autoscaler" ci-builder:"Cloud Build for runner image"; do
    g iam service-accounts describe "${sa%%:*}@${PROJECT}.iam.gserviceaccount.com" >/dev/null 2>&1 ||
      g iam service-accounts create "${sa%%:*}" --display-name="${sa#*:}"
  done
  proj_role "$BUILD_SA" roles/logging.logWriter
  proj_role "$BUILD_SA" roles/artifactregistry.writer
  proj_role "$BUILD_SA" roles/storage.objectViewer   # read the uploaded build source
  proj_role "$RUNNER_SA" roles/logging.logWriter
  proj_role "$RUNNER_SA" roles/artifactregistry.reader
  proj_role "$SCALER_SA" roles/logging.logWriter
  proj_role "$SCALER_SA" roles/artifactregistry.reader
  # The autoscaler patches the pool, which runs as RUNNER_SA.
  g iam service-accounts add-iam-policy-binding "$RUNNER_SA" --member="serviceAccount:$SCALER_SA" \
    --role=roles/iam.serviceAccountUser -q >/dev/null
}

put_secret() { # name, value on stdin
  if g secrets describe "$1" >/dev/null 2>&1; then g secrets versions add "$1" --data-file=- >/dev/null
  else g secrets create "$1" --replication-policy=automatic --data-file=- >/dev/null; fi
  g secrets add-iam-policy-binding "$1" --member="serviceAccount:$SCALER_SA" \
    --role=roles/secretmanager.secretAccessor -q >/dev/null
}

github_token() { if [ -n "${GITHUB_PAT_FILE:-}" ]; then tr -d '\n' <"$GITHUB_PAT_FILE"; else gh auth token; fi; }

secrets() {
  github_token | put_secret github-runner-token
  g secrets describe github-webhook-secret >/dev/null 2>&1 || openssl rand -hex 32 | tr -d '\n' | put_secret github-webhook-secret
  echo "secrets stored (values not shown)"
}

rotate_token() {
  github_token | put_secret github-runner-token
  # New revision so the service reads secret version "latest" again.
  g run services update "$SERVICE" --region="$REGION" --update-env-vars="TOKEN_ROTATED=$(date +%s)" >/dev/null
  echo "token rotated"
}

build() {
  local tag; tag=$(date +%Y%m%d-%H%M%S)
  local rv; rv=$(gh api repos/actions/runner/releases/latest -q .tag_name | sed 's/^v//')
  cat >"${TMPDIR:-/tmp}/runner-build.yaml" <<EOF
steps:
- name: gcr.io/cloud-builders/docker
  args: [build, --build-arg, RUNNER_VERSION=${rv}, -t, ${AR}/runner:${tag}, -t, ${AR}/runner:latest, .]
images: [${AR}/runner:${tag}, ${AR}/runner:latest]
options: {logging: CLOUD_LOGGING_ONLY}
EOF
  g builds submit "$HERE/runner" --region="$REGION" --service-account="projects/$PROJECT/serviceAccounts/$BUILD_SA" \
    --config="${TMPDIR:-/tmp}/runner-build.yaml"
  cat >"${TMPDIR:-/tmp}/scaler-build.yaml" <<EOF
steps:
- name: gcr.io/cloud-builders/docker
  args: [build, -t, ${AR}/autoscaler:${tag}, -t, ${AR}/autoscaler:latest, .]
images: [${AR}/autoscaler:${tag}, ${AR}/autoscaler:latest]
options: {logging: CLOUD_LOGGING_ONLY}
EOF
  g builds submit "$HERE/autoscaler" --region="$REGION" --service-account="projects/$PROJECT/serviceAccounts/$BUILD_SA" \
    --config="${TMPDIR:-/tmp}/scaler-build.yaml"
  echo "built runner ${rv} and autoscaler as :${tag} and :latest"
}

service_url() { g run services describe "$SERVICE" --region="$REGION" --format='value(status.url)'; }

deploy() {
  local pool_path="projects/$PROJECT/locations/$REGION/workerPools/$POOL"
  # The autoscaler must be reachable by GitHub's webhook deliveries, so the
  # service accepts unauthenticated requests at the Cloud Run layer; every
  # route authenticates in code (HMAC for the webhook, Google ID tokens of a
  # named service account for /jit and /reconcile). Same as Google's sample.
  g run deploy "$SERVICE" --region="$REGION" --image="${AR}/autoscaler:latest" \
    --service-account="$SCALER_SA" --allow-unauthenticated --concurrency=1 --max-instances=1 \
    --cpu=1 --memory=512Mi \
    --set-secrets="GITHUB_TOKEN=github-runner-token:latest,WEBHOOK_SECRET=github-webhook-secret:latest" \
    --update-env-vars="GITHUB_REPO=$REPO,WORKER_POOL=$pool_path,RUNNER_SA=$RUNNER_SA,SCHEDULER_SA=$SCALER_SA,MAX_RUNNERS=$MAX_RUNNERS"
  local url; url=$(service_url)
  # ID tokens are minted for the service URL; tell the service its own URL.
  g run services update "$SERVICE" --region="$REGION" --update-env-vars="AUDIENCE=$url" >/dev/null
  local n
  n=$(g run worker-pools describe "$POOL" --region="$REGION" --format='value(scaling.manualInstanceCount)' 2>/dev/null || true)
  g run worker-pools deploy "$POOL" --region="$REGION" --image="${AR}/runner:latest" \
    --service-account="$RUNNER_SA" --cpu="$RUNNER_CPU" --memory="$RUNNER_MEMORY" \
    --instances="${n:-0}" --set-env-vars="AUTOSCALER_URL=$url"
  # Only the autoscaler (and project owners) may change the pool.
  g run worker-pools add-iam-policy-binding "$POOL" --region="$REGION" \
    --member="serviceAccount:$SCALER_SA" --role=roles/run.developer -q >/dev/null
  echo "autoscaler: $url"
}

webhook() {
  local url; url=$(service_url)
  local secret; secret=$(g secrets versions access latest --secret=github-webhook-secret)
  jq -n --arg u "$url/" --arg s "$secret" \
    '{name:"web",active:true,events:["workflow_job"],config:{url:$u,content_type:"json",secret:$s}}' |
    gh api "repos/$REPO/hooks" --input - -q '"webhook \(.id) created"'
}

scheduler() {
  local url; url=$(service_url)
  g scheduler jobs describe ci-runner-reconcile --location="$REGION" >/dev/null 2>&1 && return 0
  g scheduler jobs create http ci-runner-reconcile --location="$REGION" --schedule="*/10 * * * *" \
    --uri="$url/reconcile" --http-method=POST \
    --oidc-service-account-email="$SCALER_SA" --oidc-token-audience="$url"
}

budget() {
  local num; num=$(gcloud projects describe "$PROJECT" --format='value(projectNumber)')
  gcloud billing budgets create --billing-project="$PROJECT" --billing-account="$BILLING_ACCOUNT" --display-name="$PROJECT monthly" \
    --budget-amount="${BUDGET}USD" --filter-projects="projects/$num" \
    --threshold-rule=percent=0.5 --threshold-rule=percent=0.9 --threshold-rule=percent=1.0
}

scale() { g run worker-pools update "$POOL" --region="$REGION" --instances="$1"; }

status() {
  echo "pool instances: $(g run worker-pools describe "$POOL" --region="$REGION" --format='value(scaling.manualInstanceCount)')"
  gh api "repos/$REPO/actions/runners" -q '.runners[] | "\(.name)\t\(.status)\tbusy=\(.busy)"'
  g logging read "resource.type=cloud_run_revision AND resource.labels.service_name=$SERVICE" \
    --limit=15 --freshness=1d --format='value(timestamp,textPayload)'
}

teardown() {
  local url; url=$(service_url || true)
  if [ -n "$url" ]; then
    gh api "repos/$REPO/hooks" -q ".[] | select(.config.url | startswith(\"$url\")) | .id" |
      while read -r id; do gh api -X DELETE "repos/$REPO/hooks/$id"; done
  fi
  g scheduler jobs delete ci-runner-reconcile --location="$REGION" -q || true
  g run worker-pools delete "$POOL" --region="$REGION" -q || true
  g run services delete "$SERVICE" --region="$REGION" -q || true
  echo "torn down; delete the whole project with: gcloud projects delete $PROJECT"
}

cmd=${1:-}; shift || true
case "$cmd" in
  setup) setup ;; secrets) secrets ;; rotate-token) rotate_token ;; build) build ;;
  deploy) deploy ;; webhook) webhook ;; scheduler) scheduler ;; budget) budget ;;
  scale) scale "$1" ;; status) status ;; teardown) teardown ;;
  *) sed -n '2,16p' "$0"; exit 1 ;;
esac
