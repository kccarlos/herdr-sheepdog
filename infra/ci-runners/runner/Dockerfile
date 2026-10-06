# Self-hosted GitHub Actions runner for Cloud Run worker pools.
# Based on Google's sample (cloud-run-samples/github-runner), changed to
# just-in-time (ephemeral, one job per registration) runners and with the
# toolchains CI needs pre-seeded in the tool cache, so actions/setup-go and
# actions/setup-node resolve locally instead of downloading every job.
ARG RUNNER_VERSION=2.337.0
FROM ghcr.io/actions/actions-runner:${RUNNER_VERSION}

ARG GO_VERSION=1.25.14
ARG NODE_VERSION=22.23.3
ARG GOLANGCI_LINT_VERSION=2.13.2

USER root
ENV DEBIAN_FRONTEND=noninteractive \
    RUNNER_TOOL_CACHE=/opt/hostedtoolcache \
    AGENT_TOOLSDIRECTORY=/opt/hostedtoolcache
# build-essential: `go test -race` needs cgo and gcc.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      build-essential ca-certificates curl git jq make unzip xz-utils zstd \
 && rm -rf /var/lib/apt/lists/*

# Tool cache layout read by @actions/tool-cache: <tool>/<version>/x64 plus an
# x64.complete marker.
RUN set -eux; \
    d=/opt/hostedtoolcache/go/${GO_VERSION}/x64; mkdir -p "$d"; \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -xz -C "$d" --strip-components=1; \
    touch "${d}.complete"; \
    d=/opt/hostedtoolcache/node/${NODE_VERSION}/x64; mkdir -p "$d"; \
    curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-x64.tar.xz" | tar -xJ -C "$d" --strip-components=1; \
    touch "${d}.complete"; \
    curl -fsSL "https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_LINT_VERSION}/golangci-lint-${GOLANGCI_LINT_VERSION}-linux-amd64.tar.gz" \
      | tar -xz -C /usr/local/bin --strip-components=1 "golangci-lint-${GOLANGCI_LINT_VERSION}-linux-amd64/golangci-lint"; \
    ln -s /opt/hostedtoolcache/go/${GO_VERSION}/x64/bin/go /usr/local/bin/go; \
    ln -s /opt/hostedtoolcache/go/${GO_VERSION}/x64/bin/gofmt /usr/local/bin/gofmt; \
    ln -s /opt/hostedtoolcache/node/${NODE_VERSION}/x64/bin/node /usr/local/bin/node; \
    ln -s /opt/hostedtoolcache/node/${NODE_VERSION}/x64/bin/npm /usr/local/bin/npm; \
    chown -R runner:runner /opt/hostedtoolcache

COPY start.sh /home/runner/start.sh
RUN chmod 0755 /home/runner/start.sh
USER runner
WORKDIR /home/runner
ENTRYPOINT ["/home/runner/start.sh"]
