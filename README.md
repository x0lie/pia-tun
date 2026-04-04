# pia-tun

A feature-rich and reliable VPN container image for PIA + WireGuard

## Features

- **Strict killswitch** - Zero-leak design with fast engagement (~25ms on amd64)
- **WireGuard speed** - Tested at greater than 95% of line speed with automatic MSS clamping
- **Reliable reconnect** - Handles outages gracefully and avoids reconnect churn
- **Port forwarding** - Manages port acquisition, keepalive, and expiry/refresh
- **Port syncing** - Automatically syncs port to qBittorrent, Deluge, Transmission, and custom endpoints
- **SOCKS5 + HTTP Proxies** - Allows other machines and containers to access VPN (optional authentication)
- **DoT Support** - Encrypt your DNS requests to further anonymize
- **Observability** - `/health`, `/ready`, `/metrics` (prometheus), and `/metrics?format=json`
- **Smart server selection** - Chooses the lowest-latency server from selected location(s), or from all locations
- **Minimal host support** - Supports WireGuard userspace (wireguard-go) and iptables-legacy auto-fallback
- **No manual auth token** - Auth token acquired automatically and kept fresh
- **Multi-architecture images** - amd64, arm64, and armv7

---
<img src="https://raw.githubusercontent.com/x0lie/pia-tun/main/img/pia-tun-image.png" alt="Title image" width="57%">

[![Latest Version](https://img.shields.io/github/v/release/x0lie/pia-tun?label=version&style=flat-round)](https://github.com/x0lie/pia-tun/releases)
[![Docker Image Size](https://img.shields.io/docker/image-size/x0lie/pia-tun/main?style=flat-round)](https://hub.docker.com/r/x0lie/pia-tun)

[![Docker Pulls](https://img.shields.io/docker/pulls/x0lie/pia-tun?style=flat-round)](https://hub.docker.com/r/x0lie/pia-tun)
[![GitHub stars](https://img.shields.io/github/stars/x0lie/pia-tun?label=github+stars&style=flat-round)](https://github.com/x0lie/pia-tun/stargazers)

## Quick Start

Versions: `latest`, `develop`, and semantic (v1, v1.0, v1.0.0)

**Copy-Paste Examples**: 
- [qbittorrent](https://github.com/x0lie/pia-tun/blob/main/docs/compose-examples/qbittorrent.md)
- [reverse-proxy (traefik)](https://github.com/x0lie/pia-tun/blob/main/docs/compose-examples/traefik.md)
- [legacy machines](https://github.com/x0lie/pia-tun/blob/main/docs/compose-examples/legacy-machines.md)

### Minimal Compose

```yaml
services:
  pia-tun:
    image: x0lie/pia-tun:latest
    container_name: pia-tun
    cap_add:
      - NET_ADMIN
    cap_drop:
      - ALL
    secrets:
      - pia_user
      - pia_pass

secrets:
  pia_user:
    file: ./secrets/pia_user
  pia_pass:
    file: ./secrets/pia_pass
```

Image also available as `ghcr.io/x0lie/pia-tun`

## More Documentation

- [environment variables](https://github.com/x0lie/pia-tun/tree/main/docs/env.md)
- [firewall behavior](https://github.com/x0lie/pia-tun/blob/main/docs/firewall.md)
- [dependent restarts](https://github.com/x0lie/pia-tun/blob/main/docs/dependent-restarts.md)
- [troubleshooting](https://github.com/x0lie/pia-tun/blob/main/docs/troubleshooting.md)

## Support

- [Issues](https://github.com/x0lie/pia-tun/issues)
- [Discussions](https://github.com/x0lie/pia-tun/discussions)

## License

[MIT License](https://github.com/x0lie/pia-tun/blob/main/LICENSE)

## Acknowledgments

Special thanks to Kevin for getting me into containerization

Built with:
- [WireGuard](https://www.wireguard.com/) - Fast, modern VPN protocol
- [Prometheus](https://prometheus.io/) - Metrics and monitoring
- [Alpine Linux](https://alpinelinux.org/) - Lightweight container base

Not affiliated with Private Internet Access
