# Rebuilds the CARE frontend bundle when a clinic changes a setting that is
# compiled in. Only the API origin stays a placeholder.
ARG FRONTEND_BUILDER
ARG FRONTEND_IMAGE

FROM ${FRONTEND_BUILDER} AS build
COPY clinic.json /app/clinic.json
RUN node /usr/local/bin/care-clinic-build.mjs

FROM ${FRONTEND_IMAGE}
RUN rm -rf /care-clinic/html
COPY --from=build /app/build /care-clinic/html
