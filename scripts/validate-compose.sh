#!/usr/bin/env bash
# Validate the Docker Compose installer surface without starting containers.
#
#   scripts/validate-compose.sh
#
# Checks:
#   - no lab-specific host addresses are committed,
#   - docker-compose.yml parses and interpolates with .env.example,
#   - computed/compute-worker use host networking,
#   - required services/profiles/healthchecks exist.
#
# Falls back to a structural YAML check when Docker Compose is unavailable.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

COMPOSE_FILE="docker-compose.yml"
ENV_FILE=".env.example"
STATUS=0

say_ok() { printf '  [OK]   %s\n' "$*"; }
say_fail() { printf '  [FAIL] %s\n' "$*"; STATUS=1; }

printf '\n==> Forbidden lab-specific addresses\n'
if grep -RnE '(^|[^0-9])(192\.168\.1\.70|100\.78\.129\.96)([^0-9]|$)' \
    "${COMPOSE_FILE}" .env.example config/daemon.json config/daemon.example.json "${ENV_FILE}" 2>/dev/null; then
    say_fail "lab-specific addresses must not be committed; use .env or config)"
else
    say_ok "no hardcoded compute-lab addresses"
fi

if [ ! -f "${ENV_FILE}" ]; then
    say_fail "${ENV_FILE} is missing"
    exit "${STATUS}"
fi
say_ok "${ENV_FILE} present"

printf '\n==> Compose interpolation\n'
TEMP_ENV=0
if [ ! -f .env ]; then
    # Compose requires the service env_file to exist. Use the example values for
    # validation exactly as a fresh install would after `cp .env.example .env`.
    cp "${ENV_FILE}" .env
    TEMP_ENV=1
    trap 'if [ "${TEMP_ENV}" = "1" ]; then rm -f .env; fi' EXIT
fi
if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    if docker compose --env-file "${ENV_FILE}" -f "${COMPOSE_FILE}" config --quiet; then
        say_ok "docker compose config succeeded with ${ENV_FILE}"
    else
        say_fail "docker compose config failed with ${ENV_FILE}"
    fi
    # The worker profile needs an enrollment token at runtime; make sure the
    # profile can at least be rendered when the token is provided.
    if COMPUTE_WORKER_TOKEN=test-token docker compose --env-file "${ENV_FILE}" -f "${COMPOSE_FILE}" --profile worker config --quiet; then
        say_ok "worker profile renders with a token"
    else
        say_fail "worker profile failed to render"
    fi
else
    echo "  [SKIP] docker compose is not installed; running structural checks only"
fi

printf '\n==> Structural checks\n'
if python3 -c 'import yaml' >/dev/null 2>&1; then
    if python3 - "${COMPOSE_FILE}" <<'PY'
import sys
import yaml

path = sys.argv[1]
with open(path, encoding="utf-8") as fh:
    doc = yaml.safe_load(fh)

problems = []
services = doc.get("services") or {}
for name in ("compute-init", "computed"):
    if name not in services:
        problems.append(f"missing service {name}")
for name in ("computed", "compute-worker"):
    svc = services.get(name) or {}
    if svc.get("network_mode") != "host":
        problems.append(f"{name} must use network_mode: host")
if (services.get("computed") or {}).get("privileged") is not True:
    problems.append("computed must be privileged")
init = services.get("compute-init") or {}
if not init.get("command"):
    problems.append("compute-init must override command to init --images")
dep = (services.get("computed") or {}).get("depends_on") or {}
if (dep.get("compute-init") or {}).get("condition") != "service_completed_successfully":
    problems.append("computed must wait for compute-init completion")
health = (services.get("computed") or {}).get("healthcheck") or {}
test = health.get("test") or []
if not any("/api/v1/ready" in str(part) for part in test):
    problems.append("computed healthcheck must use /api/v1/ready")
profiles = (services.get("compute-worker") or {}).get("profiles") or []
if "worker" not in profiles:
    problems.append("compute-worker must be in the worker profile")
if "gui" not in ((services.get("compute-gui") or {}).get("profiles") or []):
    problems.append("compute-gui must be in the gui profile")
if "dev" not in ((services.get("compute-dev") or {}).get("profiles") or []):
    problems.append("compute-dev must be in the dev profile")

if problems:
    for p in problems:
        print(f"  [FAIL] {p}")
    sys.exit(1)
print("  [OK]   compose structure matches the installer contract")
PY
    then
        :
    else
        say_fail "compose structure validation failed"
    fi
else
    echo "  [SKIP] PyYAML is not installed; cannot run structural checks"
fi

printf '\n==> Shell syntax\n'
for script in scripts/preflight.sh scripts/host-prepare.sh scripts/setup-host.sh scripts/validate-compose.sh; do
    if [ -f "${script}" ] && bash -n "${script}"; then
        say_ok "bash -n ${script}"
    fi
done

if [ "${STATUS}" -ne 0 ]; then
    printf '\nCompose validation failed.\n'
    exit 1
fi
printf '\nCompose validation passed.\n'
