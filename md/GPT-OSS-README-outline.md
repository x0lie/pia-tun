# Docker Image README Outline: PIA + WireGuard

## 1. Overview
- Brief description of the image – a drop‑in VPN client that combines **Private Internet Access (PIA)** with **WireGuard** for maximum speed and flexibility.
- High‑level value proposition (privacy, bypass blocks, consistent performance).

## 2. Prerequisites
- Minimum Docker version and platform (Linux, macOS, Windows, ARM).
- Host networking requirements (e.g., need to expose certain ports, DNS configuration).
- Optional dependencies (e.g., Docker Compose, `jq`).

## 3. Quick Start
- One‑liner to pull the image:
  ```bash
  docker pull ghcr.io/your-org/pia-wireguard:latest
  ```
- Minimal `docker run` example with environment variables placeholders.
- Suggested `docker compose` snippet (single service example).

## 4. Usage
### 4.1 Environment Variables
- `PIA_USER`, `PIA_PASS`: PIA credentials or a credential file.
- `WG_CONFIG`: Path or name of the WireGuard config to use.
- `DEFAULT_PORT`: Default outbound port.
### 4.2 Volumes
- Mount `/etc/wireguard/` for custom `.conf` files.
- Optional mount for an SSL/CA bundle.
### 4.3 Network Modes
- `host` vs. `bridge`. When to use each.
- Explanation of `--network=host` for faster routing.
### 4.4 Example Compose
````yaml
version: "3.9"
services:
  pia-wg:
    image: ghcr.io/your-org/pia-wireguard:latest
    environment:
      - PIA_USER=your_user
      - PIA_PASS=your_pass
      - WG_CONFIG=wg0.conf
    volumes:
      - ./configs/wg/:/etc/wireguard/
    network_mode: host
````

## 5. Configuration
- How to generate or supply a WireGuard config.
- How the image handles PIA authentication automatically.
- Advanced settings:
  - `ALLOWED_CIDR` to restrict traffic.
  - `PEER_LIMITS` for bandwidth shaping.
- TLS/SSL options for `wg-quick` or `wg`.

## 6. Advanced Features
- **Port Forwarding**: Description of auto‑forward via PIA API scripts.
- **Health Checks & Metrics**: How the image reports health, expected outputs.
- **Logging**: Default log path, log rotation flags, optional `--log-driver`.
- **Custom Scripts**: Hook points for entrypoint scripts.

## 7. Security Considerations
- Runtime user (`--user=1000:1000`) vs. root.
- Security options: `--cap-drop` (except necessary caps), `--security-opt` (seccomp/profile), `--read-only` filesystem.
- Best practice for secrets (Docker secrets, env vars, `~/.config/pia-secrets`).

## 8. Troubleshooting
- List of common pitfalls (authentication failures, `Permission denied`, `WireGuard config not found`).
- Suggested debug commands (`docker logs -f`, `--entrypoint /bin/bash`).
- Quick FAQ.

## 9. Building Locally
- `docker build -t pia-wireguard .`.
- Build arguments to customize PIA package path and version.

## 10. Contributing
- Repository guidelines, contribution workflow, issue labeling.
- Code formatting, testing in container environment.

## 11. License
- Specify license text (e.g., MIT License) and reference the LICENSE file.

## 12. Contact & Support
- Link to the GitHub issue tracker.
- Discord/Slack community link, mailing list, or official PIA support channel.
