#!/usr/bin/env bash
# Cloud Run (Option 1) entrypoint: start the CPU OVMS embedder in the background,
# wait until its REST port is up, then exec the MCP server in the foreground so it
# is PID 1 and receives Cloud Run's SIGTERM for graceful shutdown.
set -euo pipefail

# OVMS serves the baked BGE-M3 INT8 model on :8000 (the server reaches it via
# BANHMI_EMBED_ENDPOINT=http://127.0.0.1:8000/v3, baked into the image).
/ovms/bin/ovms --rest_port 8000 --config_path /config/bge-m3-config.json &

# Wait (max ~90s) for OVMS to bind its REST port, so the server only starts
# accepting traffic once query embedding is available. bash /dev/tcp avoids a
# curl dependency.
for _ in $(seq 1 90); do
  if (exec 3<>/dev/tcp/127.0.0.1/8000) 2>/dev/null; then
    exec 3>&- 3<&-
    echo "entrypoint: OVMS embedder is up on :8000"
    break
  fi
  sleep 1
done

exec /usr/local/bin/server
