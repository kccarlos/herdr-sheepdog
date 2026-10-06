#!/bin/bash
# Worker-pool entrypoint: loop forever, one job per just-in-time registration.
#
# The runner never holds the GitHub token. It proves its identity to the
# autoscaler service with a Google ID token from the metadata server (its
# service account), and the autoscaler returns a JIT runner config, which is
# good for exactly one job. After the job the runner exits, its work
# directory is wiped, and the loop registers again.
set -uo pipefail

: "${AUTOSCALER_URL:?AUTOSCALER_URL must be set}"
MD=http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity
cd /home/runner

child=
on_term() {
  echo "SIGTERM: stopping runner"
  [ -n "$child" ] && kill -TERM "$child" 2>/dev/null
  exit 143
}
trap on_term TERM INT

while true; do
  name="cloudrun-$(tr -dc 'a-z0-9' </dev/urandom | head -c 8)"
  idt=$(curl -fsS -H 'Metadata-Flavor: Google' "${MD}?audience=${AUTOSCALER_URL}") || { echo "no ID token, retrying"; sleep 10; continue; }
  jit=$(curl -fsS -X POST -H "Authorization: Bearer ${idt}" -H 'Content-Type: application/json' \
          -d "{\"name\":\"${name}\"}" "${AUTOSCALER_URL}/jit") || { echo "JIT config request failed, retrying"; sleep 15; continue; }
  echo "registered ${name}; waiting for a job"
  ./run.sh --jitconfig "${jit}" &
  child=$!
  wait "$child"
  echo "runner ${name} exited ($?)"
  child=
  # Fresh state for the next job.
  rm -rf _work _diag .runner .credentials .credentials_rsaparams
  sleep 1
done
