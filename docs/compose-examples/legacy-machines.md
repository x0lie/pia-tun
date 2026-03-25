# Legacy and Minimal machines (Synology NAS users, etc.)

Many older or minimal systems (especially pre-5.6 kernels, Synology NAS, etc.) have incomplete or conflicting support for modern networking features (nf_tables and WireGuard kernel module).

## Legacy Backend Kernel Requirements

- iptables-legacy: ip_tables and ip6_tables
- WireGuard userspace: no kernel module requirements

## Auto-detection

- Auto-detection is pretty accurate (probes for capability, chooses modern if present)
  - If Docker rules are written to iptables-legacy, pia-tun will follow Docker's example
- Feel free to override auto-detection with `IPT_BACKEND={nft,legacy}` and `WG_BACKEND={kernel,userspace}`

See [troubleshooting.md](../troubleshooting.md) for more information on module probing if you have issues.

```yaml
services:
  pia-tun:
    image: x0lie/pia-tun:latest
    container_name: pia-tun
    cap_add:
      - NET_ADMIN
      - NET_RAW       # required for iptables-legacy
    cap_drop:
      - ALL
    secrets:
      - pia_user
      - pia_pass
    devices:          # required for wireguard userspace
      - /dev/net/tun:/dev/net/tun

secrets:
  pia_user:
    file: ./secrets/pia_user
  pia_pass:
    file: ./secrets/pia_pass
```

See [environment variables](../env.md) for more configuration
