# syntax=docker/dockerfile:1
#
# CARE frontend image for CARE Clinic. Builds unmodified care_fe source and
# compiles every setting except the hostname-derived API origin.
#
#   docker buildx build images/frontend \
#     --build-context care_fe=<care_fe checkout> \
#     --target builder -t <registry>/care-clinic-frontend-builder:<sha>   # local rebuild fallback
#   docker buildx build images/frontend \
#     --build-context care_fe=<care_fe checkout> \
#     -t <registry>/care-clinic-frontend:<sha>
#
# clinic.json must sit next to this file: images/scripts/frontend-env.mjs writes it.
# CI builds each architecture on a native runner: unlike care_fe's own
# Dockerfile the builder stage is not pinned to the build platform, so the
# published builder image runs on the clinic's architecture.

ARG NODE_IMAGE=node:22-bookworm-slim
ARG NGINX_IMAGE=nginx:stable-alpine

FROM ${NODE_IMAGE} AS builder
WORKDIR /app
ENV NODE_OPTIONS=--max-old-space-size=4096
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=care_fe package.json package-lock.json ./
RUN npm install --ignore-scripts
COPY --from=care_fe . .
RUN npm run postinstall
COPY build.mjs /usr/local/bin/care-clinic-build.mjs

FROM builder AS build
COPY clinic.json /app/clinic.json
RUN node /usr/local/bin/care-clinic-build.mjs

FROM ${NGINX_IMAGE}
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --chmod=0755 40-care-api-url.sh /docker-entrypoint.d/40-care-api-url.sh
COPY --from=build /app/build /care-clinic/html
RUN rm -rf /usr/share/nginx/html/*
EXPOSE 80
