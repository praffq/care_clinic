# syntax=docker/dockerfile:1
#
# CARE frontend for CARE Clinic, built from unmodified care_fe source (named
# build context "care_fe") with deployments/ as the main context:
#
#   --target builder  care-frontend-builder: care_fe at /app with node_modules
#                     and /usr/local/bin/care-build-remote. The engine rebuilds
#                     the app (new .env.local, `npm run build`) and builds
#                     plugin remotes from it in the clinic VM.
#   (default)         care-frontend: nginx serving the app, with the curated
#                     plugin remotes at /plugins/<slug>/.
#
# PLUGIN_REMOTES lists "slug=git-url@rev" words. Not pinned to $BUILDPLATFORM:
# the builder image runs on the clinic's architecture, so CI builds each
# architecture on a native runner. See scripts/dev-images.sh.

ARG NODE_IMAGE=node:22-bookworm-slim
ARG NGINX_IMAGE=nginx:stable-alpine

FROM ${NODE_IMAGE} AS builder
WORKDIR /app
ENV NODE_OPTIONS=--max-old-space-size=4096
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=care_fe package.json package-lock.json ./
RUN npm install --ignore-scripts --no-audit --no-fund
COPY --from=care_fe . .
RUN npm run postinstall
COPY --chmod=0755 care-build-remote.sh /usr/local/bin/care-build-remote

FROM builder AS build
COPY frontend.env /app/.env.local
RUN npm run build

FROM builder AS remotes
ARG PLUGIN_REMOTES=""
RUN mkdir -p /plugins \
 && for r in $PLUGIN_REMOTES; do \
      slug=${r%%=*}; src=${r#*=}; \
      care-build-remote "$slug" "${src%@*}" "${src##*@}" "/plugins/$slug" || exit 1; \
    done

FROM ${NGINX_IMAGE}
RUN rm -rf /usr/share/nginx/html/*
COPY frontend-nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/build /usr/share/nginx/html
COPY --from=remotes /plugins /usr/share/nginx/html/plugins
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
