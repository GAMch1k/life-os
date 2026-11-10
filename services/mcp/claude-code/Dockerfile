# check=skip=InvalidDefaultArgInFrom
# Claude Code for a release install (docs/install/ai-processing.md). Life OS's
# published images never contain Claude Code: ./setup.sh claude builds this on
# the owner's own server as lifeos-processor-claude:<version>. The final stage
# is the release's processor image (LIFE_OS_PROCESSOR_BASE: the image the
# release's compose.yaml runs for that version, by digest when the registry
# gave one) with Claude Code's native binary added, as install.sh takes it
# from the official npm package at the version and integrity package-lock.json
# pins. tools/build-install-bundle writes this directory's files into the
# release's setup.sh; here it can be built on its own:
#   docker build --build-arg LIFE_OS_PROCESSOR_BASE=<processor image> services/mcp/claude-code
# No default: the base is always the release image setup.sh names (the check
# directive above keeps BuildKit from warning about that).
ARG LIFE_OS_PROCESSOR_BASE

# The processor image's own base (services/mcp/Dockerfile), with npm.
FROM node:22-alpine@sha256:0e1754758c2df4cfa3e68d59e2e1b7374bb7fd9ea6328eaaacf15cc934af8202 AS claude-code
COPY package.json package-lock.json install.sh /opt/claude-code/
RUN sh /opt/claude-code/install.sh /out

FROM ${LIFE_OS_PROCESSOR_BASE}
LABEL org.opencontainers.image.licenses="AGPL-3.0-only AND LicenseRef-Claude-Code" \
      org.opencontainers.image.description="Life OS's processor with Claude Code, which ./setup.sh claude downloaded from Anthropic's npm package and added on this server; Claude Code is Anthropic's software, under Anthropic's terms"
COPY --from=claude-code /out/claude /usr/local/bin/claude
RUN HOME=/tmp/lifeos-build claude --version && rm -rf /tmp/lifeos-build
