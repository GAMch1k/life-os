# The gateway: nginx with Life OS's configuration built in, so that a server
# runs it without a checkout (P20-02). At every start the image renders the
# template into /etc/nginx/conf.d/default.conf, replacing only the LIFE_OS_*
# names (NGINX_ENVSUBST_FILTER) that compose.yaml sets from .env; just before
# that, server-names.envsh tidies the host names (docs/architecture/networking.md).
# A change to either file takes a rebuild of this image, then the gateway's
# recreation (docs/operations/deployment.md).
FROM nginx:1.30-alpine@sha256:583b7f2fce1cdb8da5ad5c39ebd3c33b98b87d493190e548bdee1fa8c619c311
# The image's source and license; on ghcr.io this links its package to the
# repository (docs/operations/releases.md).
LABEL org.opencontainers.image.source="https://github.com/GAMch1k/life-os" \
      org.opencontainers.image.licenses="AGPL-3.0-only"

ENV NGINX_ENVSUBST_FILTER=^LIFE_OS_
RUN mkdir -p /etc/nginx/templates
COPY --chmod=0644 life-os.conf.template /etc/nginx/templates/default.conf.template
# The entrypoint sources only executable *.envsh files.
COPY --chmod=0755 server-names.envsh /docker-entrypoint.d/16-lifeos-server-names.envsh
