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
    say_ok "no hardcoded lab-specific addresses"
fi

if [ ! -f "${ENV_FILE}" ]; then
    say_fail "${ENV_FILE} is missing"
    exit "${STATUS}"
fi
say_ok "${ENV_FILE} present"

printf '\n==> Compose interpolation\n'
if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    # First verify out-of-the-box `docker compose up` with no .env file when none exists,
    # or explicitly with /dev/null as env-file if a developer .env exists locally.
    EMPTY_ENV="$(mktemp)"
    trap 'rm -f "${EMPTY_ENV}"' EXIT
    if docker compose --env-file "${EMPTY_ENV}" -f "${COMPOSE_FILE}" config --quiet; then
        say_ok "docker compose config succeeded with zero .env overrides"
    else
        say_fail "docker compose config failed without .env"
    fi
    if docker compose --env-file "${ENV_FILE}" -f "${COMPOSE_FILE}" config --quiet; then
        say_ok "docker compose config succeeded with ${ENV_FILE}"
    else
        say_fail "docker compose config failed with ${ENV_FILE}"
    fi
    # Empty COMPUTE_ADMIN_PASSWORD= (e.g. with COMPUTE_ADMIN_SECRET) must stay
    # empty instead of falling back to "admin".
    ADMIN_SECRET_ENV="$(mktemp)"
    printf 'COMPUTE_ADMIN_PASSWORD=\nCOMPUTE_ADMIN_SECRET=test-secret\n' > "${ADMIN_SECRET_ENV}"
    if docker compose --env-file "${ADMIN_SECRET_ENV}" -f "${COMPOSE_FILE}" config | grep -Eq 'COMPUTE_ADMIN_PASSWORD:[[:space:]]*""$'; then
        say_ok "explicitly empty COMPUTE_ADMIN_PASSWORD= is preserved"
    else
        say_fail "explicitly empty COMPUTE_ADMIN_PASSWORD= was overwritten with a default"
    fi
    rm -f "${ADMIN_SECRET_ENV}"
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
import re
import sys
import yaml

path = sys.argv[1]
with open(path, encoding="utf-8") as fh:
    doc = yaml.safe_load(fh)

problems = []
services = doc.get("services") or {}
if "computed" not in services:
    problems.append("missing service computed")
if "compute-init" in services:
    problems.append("compute-init must not be in docker-compose.yml; host/image bootstrap belongs in scripts/install.sh")
for name, svc in services.items():
    if isinstance(svc, dict) and svc.get("env_file"):
        problems.append(f"{name} must not require env_file (.env should remain optional)")
for name in ("computed", "compute-worker"):
    svc = services.get(name) or {}
    if svc.get("network_mode") != "host":
        problems.append(f"{name} must use network_mode: host")
if (services.get("computed") or {}).get("privileged") is not True:
    problems.append("computed must be privileged")
computed_env = (services.get("computed") or {}).get("environment") or {}
if computed_env.get("COMPUTE_ADMIN_PASSWORD") != "${COMPUTE_ADMIN_PASSWORD-admin}":
    problems.append("computed COMPUTE_ADMIN_PASSWORD must use ${COMPUTE_ADMIN_PASSWORD-admin}")
with open("internal/config/env.go", encoding="utf-8") as env_fh:
    env_vars = set(re.findall(r'"(COMPUTE_[A-Z0-9_]+)"', env_fh.read()))
missing_env = sorted(v for v in env_vars if v not in computed_env)
if missing_env:
    problems.append(f"computed environment missing internal/config/env.go vars: {', '.join(missing_env)}")
dep = (services.get("computed") or {}).get("depends_on") or {}
if "compute-init" in dep:
    problems.append("computed must not depend on compute-init")
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
if [ ! -x "scripts/install.sh" ]; then
    say_fail "scripts/install.sh is missing or not executable"
fi
for script in scripts/install.sh scripts/preflight.sh scripts/host-prepare.sh scripts/setup-host.sh scripts/build-kernel.sh scripts/build-rootfs.sh scripts/build-rootfs-chroot.sh scripts/validate-compose.sh; do
    if [ -f "${script}" ] && bash -n "${script}"; then
        say_ok "bash -n ${script}"
    fi
done

if [ "${STATUS}" -ne 0 ]; then
    printf '\nCompose validation failed.\n'
    exit 1
fi
printf '\nCompose validation passed.\n'
