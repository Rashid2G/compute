# Preview Deployment Recipes

Production-ready patterns and scripts for deploying ephemeral per-PR previews using `compute` across popular technology stacks.

---

## 1. Node.js / Next.js / Vite Web Application

### Scenario
A frontend React/Vite or Next.js PR that needs to be built and served on port `3000` with hot live preview.

### Deployment Script
```bash
#!/usr/bin/env bash
set -euo pipefail

PR_NUMBER="${1:?PR number required}"
REPO_URL="${2:?Git repo URL required}"
BRANCH="${3:?Git branch required}"
SANDBOX_NAME="pr-${PR_NUMBER}-frontend"

# 1. Provision & Start Sandbox
echo "==> Provisioning sandbox ${SANDBOX_NAME}..."
compute sandbox create --name "${SANDBOX_NAME}" --vcpus 2 --memory 2048 --disk 10240
compute sandbox start "${SANDBOX_NAME}"

# 2. Clone and Build
echo "==> Cloning and building branch ${BRANCH}..."
compute sandbox exec "${SANDBOX_NAME}" -- bash -c "
  git clone --depth 1 -b ${BRANCH} ${REPO_URL} /app && \
  cd /app && \
  npm install && \
  npm run build
"

# 3. Start Server via Background Process API
echo "==> Starting web server..."
compute sandbox exec "${SANDBOX_NAME}" -- bash -c "
  cd /app && nohup npm run preview -- --port 3000 --host 0.0.0.0 > /var/log/app.log 2>&1 &
"

# 4. Verify Local Readiness
echo "==> Awaiting health check on port 3000..."
compute sandbox exec "${SANDBOX_NAME}" -- bash -c "
  for i in \$(seq 1 30); do
    if curl -s http://127.0.0.1:3000 >/dev/null; then
      echo 'Server listening!'; exit 0
    fi
    sleep 1
  done
  echo 'Server failed to start:' && cat /var/log/app.log && exit 1
"

# 5. Enable Public Ingress
curl -s -X POST "http://${COMPUTE_DAEMON:-127.0.0.1:18080}/api/v1/network/port-forwards" \
  -H "Authorization: Bearer ${COMPUTE_API_KEY}" \
  -H "Content-Type: application/json" \
  -d "{\"vm_id\": \"${SANDBOX_NAME}\", \"guest_port\": 3000, \"public\": true}"

DAEMON_HOST="${COMPUTE_DAEMON:-127.0.0.1:18080}"
PREVIEW_URL="http://${DAEMON_HOST}/preview/${SANDBOX_NAME}/3000/"

echo "=================================================="
echo " Preview URL: ${PREVIEW_URL}"
echo "=================================================="
```

---

## 2. Python FastAPI / Flask Backend Service

### Scenario
A Python backend API that exposes Swagger documentation and API endpoints on port `8000`.

### Deployment Script
```bash
#!/usr/bin/env bash
set -euo pipefail

PR_NUMBER="${1:?PR number required}"
REPO_URL="${2:?Git repo URL required}"
BRANCH="${3:?Git branch required}"
SANDBOX_NAME="pr-${PR_NUMBER}-api"

compute sandbox create --name "${SANDBOX_NAME}" --vcpus 2 --memory 2048
compute sandbox start "${SANDBOX_NAME}"

compute sandbox exec "${SANDBOX_NAME}" -- bash -c "
  git clone --depth 1 -b ${BRANCH} ${REPO_URL} /app && \
  cd /app && \
  python3 -m venv /venv && \
  /venv/bin/pip install --upgrade pip && \
  /venv/bin/pip install -r requirements.txt
"

compute sandbox exec "${SANDBOX_NAME}" -- bash -c "
  cd /app && \
  nohup /venv/bin/uvicorn main:app --host 0.0.0.0 --port 8000 > /var/log/api.log 2>&1 &
"

# Make public
curl -s -X POST "http://${COMPUTE_DAEMON:-127.0.0.1:18080}/api/v1/network/port-forwards" \
  -H "Authorization: Bearer ${COMPUTE_API_KEY}" \
  -H "Content-Type: application/json" \
  -d "{\"vm_id\": \"${SANDBOX_NAME}\", \"guest_port\": 8000, \"public\": true}"

DAEMON_HOST="${COMPUTE_DAEMON:-127.0.0.1:18080}"
echo "Swagger Docs: http://${DAEMON_HOST}/preview/${SANDBOX_NAME}/8000/docs"
```

---

## 3. Go Web API / Service

### Scenario
A compiled Go binary listening on port `8080`.

### Deployment Script
```bash
#!/usr/bin/env bash
set -euo pipefail

SANDBOX_NAME="pr-${1}-go-srv"

compute sandbox create --name "${SANDBOX_NAME}" --vcpus 2 --memory 2048
compute sandbox start "${SANDBOX_NAME}"

compute sandbox exec "${SANDBOX_NAME}" -- bash -c "
  sudo apt-get update -y && sudo apt-get install -y golang && \
  git clone --depth 1 https://github.com/my-org/my-go-service.git /app && \
  cd /app && \
  go build -o /app/server ./cmd/server && \
  nohup /app/server -listen :8080 > /var/log/server.log 2>&1 &
"

curl -s -X POST "http://${COMPUTE_DAEMON:-127.0.0.1:18080}/api/v1/network/port-forwards" \
  -H "Authorization: Bearer ${COMPUTE_API_KEY}" \
  -H "Content-Type: application/json" \
  -d "{\"vm_id\": \"${SANDBOX_NAME}\", \"guest_port\": 8080, \"public\": true}"

DAEMON_HOST="${COMPUTE_DAEMON:-127.0.0.1:18080}"
echo "Go Service Preview: http://${DAEMON_HOST}/preview/${SANDBOX_NAME}/8080/"
```

---

## 4. Full Teardown Hook (GitHub Actions / CI cleanup)

```bash
#!/usr/bin/env bash
set -euo pipefail

PR_NUMBER="${1:?PR number required}"

# Stop and delete any sandboxes matching the PR pattern
compute sandbox stop "pr-${PR_NUMBER}-*" || true
compute sandbox delete "pr-${PR_NUMBER}-*" || true

# Prune host network allocations
compute network prune
echo "Cleanup completed for PR #${PR_NUMBER}"
```
