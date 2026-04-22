# Environment variables

`PIA_USER` and `PIA_PASS` are the only requirements (unless using secrets)

## VPN Settings

| Variable        | Description                                                                                           | Default  |
|-----------------|-------------------------------------------------------------------------------------------------------|----------|
| `PIA_USER`      | PIA username (or use `/run/secrets/pia_user`)                                                         | Required |
| `PIA_PASS`      | PIA password (or use `/run/secrets/pia_pass`)                                                         | Required |
| `PIA_LOCATIONS` | Comma-separated locations (e.g., `ca_ontario,ca_toronto`). Tests latency and selects the best server. | `all`    |
| `LOG_LEVEL`     | Logging verbosity: `error`, `info`, `debug`, `trace`                                                  | `info`   |
| `TZ`            | Changes logging timestamps from UTC to a specified timezone (e.g., `America/New_York`)                | None     |
| `WG_BACKEND`    | WireGuard implementation: `kernel` (faster) or `userspace` (wireguard-go). Auto-detected if not set.  | Auto     |
| `MTU`           | Max Packet Size for the WireGuard Interface (pia0)                                                    | 1420     |

## Network/Firewall

| Variable         | Description                                                                                                                    | Default                         |
|------------------|--------------------------------------------------------------------------------------------------------------------------------|---------------------------------|
| `LOCAL_NETWORKS` | CIDR ranges for tunnel bypass. Supports IPv4 and IPv6 (e.g., `192.168.1.0/24,fd00::/64`) or `all` or `none`                    | `auto`                          |
| `IPT_BACKEND`    | iptables backend: `nft` or `legacy`. Auto-detected if not set.                                                                 | Auto                            |
| `DNS`            | Supports `pia`, `system`, DoT (e.g., `tls://one.one.one.one,dns.mullvad.net`), or Do53 (e.g., `1.1.1.1,8.8.8.8`). Round-robin. | `pia`                           |
| `BOOTSTRAP_DNS`  | Supports Do53 only. Do not set IPs that overlap with `DNS`.                                                                    | `149.112.112.9, 149.112.112.11` |

### LOCAL_NETWORKS explained

- Allows bidirectional access to containers and machines on the specified networks.
- `LOCAL_NETWORKS=all` is the same as `LOCAL_NETWORKS=10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, fc00::/7`.
- Automatically includes the attached container network(s) unless set to `none`.
  - On k8s, with CNIs like Cilium and Calico, auto-detection does not work - set to your pod CIDR or `all` for access.
- Accepts both private/public and IPv4/IPv6 CIDRs (single addresses need /32).
- If using a reverse proxy on Docker, setting `LOCAL_NETWORKS` is often unnecessary.
- If `DNS=pia` and `LOCAL_NETWORKS` includes PIA's DNS, DNS routes take priority (routes through tunnel).

### DNS and BOOTSTRAP_DNS explained

- `BOOTSTRAP_DNS` is what is used to resolve www.privateinternetaccess.com and serverlist.piaservers.net.
  - Useful in specific cases, like for setting granular CiliumNetworkPolicy (toFQDNs).
  - Should be left default by most users, and should not overlap with `DNS`.
- `DNS=system` should only be used if you understand what is downstream of your default /etc/resolv.conf.
  - With Docker this is typically a localhost loopback, which is allowed regardless of `LOCAL_NETWORKS`.
  - It is easy to leak your DNS with this setting, most users should avoid `DNS=system`.

## Port Forwarding & Syncing

| Variable     | Description                                                                                                           | Default             |
|--------------|-----------------------------------------------------------------------------------------------------------------------|---------------------|
| `PF_ENABLED` | Enable PIA port forwarding. Automatically enabled when `PS_CLIENT` or `PS_SCRIPT` is set.                             | `false`             |
| `PS_CLIENT`  | Client type: `qbittorrent`, `transmission`, `deluge`                                                                  | None                |
| `PS_URL`     | `PS_CLIENT` API endpoint. Auto-set to localhost:{default-port} based on PS_CLIENT setting.                            | Auto                |
| `PS_USER`    | Client username (or use `/run/secrets/ps_user`)                                                                       | None                |
| `PS_PASS`    | Client password (or use `/run/secrets/ps_pass`)                                                                       | None                |
| `PS_SCRIPT`  | Custom script executed after port refresh (use `{PORT}` placeholder). Can be used alongside or instead of `PS_CLIENT` | None                |
| `PORT_FILE`  | File forwarded port is written to                                                                                     | `/run/pia-tun/port` |

- `PS_URL` defaults to "http://localhost:{8080,9091,8112}" when `PS_CLIENT` set to qbittorrent, transmission, or deluge respectively.
  - If you change the client port from default, say 8081, set `PS_URL=http://localhost:8081`
- For clients like qBit you can enable "Bypass authentication for clients on localhost" in client settings to eliminate the need for credentials.

### Custom Port Update Script

Execute custom scripts when the forwarded port changes using `PS_SCRIPT`. This works independently of `PS_CLIENT` (you can use both).
Can be useful as a webhook, or to update clients not supported by `PS_CLIENT`. Script will be retried indefinitely until success, like with `PS_CLIENT`.

**Using PS_SCRIPT**:
```yaml
environment:
  - PS_SCRIPT=/app/your-script.sh {PORT}
volumes:
  - ./your-script.sh:/app/your-script.sh
```

## Proxy Settings

| Variable             | Description                                                      | Default |
|----------------------|------------------------------------------------------------------|---------|
| `SOCKS5_ENABLED`     | Enables SOCKS5 proxy                                             | `false` |
| `SOCKS5_PORT`        | SOCKS5 listen port                                               | `1080`  |
| `HTTP_PROXY_ENABLED` | Enables HTTP proxy                                               | `false` |
| `HTTP_PROXY_PORT`    | HTTP proxy listen port                                           | `8888`  |
| `PROXY_USER`         | Proxy authentication username (or use `/run/secrets/proxy_user`) | None    |
| `PROXY_PASS`         | Proxy authentication password (or use `/run/secrets/proxy_pass`) | None    |

## Health Monitoring & Reconnects

| Variable            | Description                                                | Default |
|---------------------|------------------------------------------------------------|---------|
| `HC_INTERVAL`       | Health check frequency (seconds)                           | `10`    |
| `HC_FAILURE_WINDOW` | Time in seconds for disconnected state before reconnection | `30`    |

- Health checks are to NIST servers on daytime protocol (port 13)
- `HC_INTERVAL` and `HC_FAILURE_WINDOW` defaults are fine for most users
- You can lengthen `HC_FAILURE_WINDOW` if overly reactive to your ISP drops

## Metrics & Observability

| Variable          | Description                                                              | Default |
|-------------------|--------------------------------------------------------------------------|---------|
| `METRICS_ENABLED` | Enable Prometheus metrics endpoint (listen on localhost only by default) | `true`  |
| `METRICS_PORT`    | Metrics server port                                                      | `9090`  |
| `INSTANCE_NAME`   | Prometheus label for users running more than one container               | None    |
