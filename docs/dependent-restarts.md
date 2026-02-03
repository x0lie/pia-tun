**1. Port File Monitoring**

Watch the port file for changes:
```bash
# Using inotifywait
inotifywait -m /run/pia-tun/port | while read; do
    NEW_PORT=$(cat /run/pia-tun/port)
    echo "Port changed to: $NEW_PORT"
    # Restart your service or update configuration
done
```

**2. Webhook Integration**

Use `PS_CMD` to POST to your own webhook service with {PORT}:
```yaml
environment:
  - PS_CMD=curl -s "http://localhost:8081/api/v2/app/setPreferences" --data "json={\"listen_port\":{PORT}}"
```

Your chosen API receives a POST request when the port changes, allowing you to:
- Restart dependent containers (via Docker API)
- Update load balancer configuration
- Trigger custom automation

**3. Docker Compose Healthcheck**

For coordinating initial startup only (not for liveness monitoring):
```yaml
services:
  pia-tun:
    healthcheck:
      test: ["CMD", "test", "-f", "/tmp/killswitch_up"]
      interval: 5s
      timeout: 3s
      retries: 3
      start_period: 30s

  dependent:
    network_mode: "service:pia-tun"
    depends_on:
      pia-tun:
        condition: service_healthy
```