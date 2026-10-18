# DEMO ONLY. Backend "B" = backend A plus one harmless no-op Django migration,
# so the update demo can show a release that carries a migration.
ARG BASE
FROM ${BASE}

# Chain onto the current emr leaf migration of A (highest-numbered file) so
# the graph stays linear. RunPython.noop in both directions: touches nothing.
RUN set -eu; \
    dir=care/emr/migrations; \
    leaf=$(ls "$dir" | grep -E '^[0-9]{4}_.*\.py$' | sort | tail -n 1); \
    leaf=${leaf%.py}; \
    next=$(printf '%04d' "$(expr "${leaf%%_*}" + 1)"); \
    printf '%s\n' \
      '# DEMO ONLY (CARE Clinic backend-B): no-op migration.' \
      'from django.db import migrations' \
      '' \
      '' \
      'class Migration(migrations.Migration):' \
      "    dependencies = [(\"emr\", \"$leaf\")]" \
      '    operations = [migrations.RunPython(migrations.RunPython.noop, migrations.RunPython.noop)]' \
      > "$dir/${next}_care_clinic_demo_noop.py"; \
    echo "added $dir/${next}_care_clinic_demo_noop.py (depends on $leaf)"

LABEL org.ohc.care-clinic.demo="backend-B: A + no-op migration"
