# DEMO ONLY. Backend "C-broken" = backend A that can never become healthy, for
# the failed-health -> rollback demo. Compose overrides the entrypoint with
# `bash start.sh` (and celery_*.sh), so those scripts and the healthcheck are
# replaced too, not just ENTRYPOINT.
ARG BASE
FROM ${BASE}

RUN set -eu; \
    for f in start.sh celery_worker.sh celery_beat.sh healthcheck.sh; do \
      printf '#!/bin/sh\necho "care-clinic demo: backend-C-broken always fails ($0)" >&2\nexit 1\n' > "$f"; \
      chmod 0755 "$f"; \
    done

ENTRYPOINT ["/bin/sh", "-c", "echo 'care-clinic demo: backend-C-broken always fails' >&2; exit 1"]
CMD []

LABEL org.ohc.care-clinic.demo="backend-C-broken: exits 1 on start"
