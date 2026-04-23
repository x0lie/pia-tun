# qBittorrent example

## Note on qBittorrent authentication

- Accepts Docker secrets (ps_user and ps_pass) or env var (PS_USER and PS_PASS)
- You can also select "Bypass authentication for clients on localhost" in qBit settings (Options > WebUI > Authentication) to skip authentication for pia-tun

## Note on PIA_LOCATIONS

- `PIA_LOCATIONS=all` (default) works well, but latency-based selection doesn't guarantee highest throughput
  - Path quality, peering quality, and network congestion all have an effect on throughput
- Most Americans will see the highest speeds to PIA's Canadian servers rather than Mexico or South America, for example

```yaml
services:
  pia-tun:
    image: x0lie/pia-tun:latest
    container_name: pia-tun
    cap_add:
      - NET_ADMIN
    cap_drop:
      - ALL
    environment:
      - PIA_LOCATIONS=ca_ontario,ca_ontario-so,ca_toronto
      - PS_CLIENT=qbittorrent
      - LOCAL_NETWORKS=192.168.1.0/24   # Your LAN CIDR here
    secrets:
      - pia_user
      - pia_pass
      - ps_user
      - ps_pass
    ports:
      - 8080:8080                       # Allows access to qBit webui from LAN via host IP

  qbittorrent:
    image: lscr.io/linuxserver/qbittorrent:latest
    container_name: qbittorrent
    network_mode: "service:pia-tun"     # Allows qBit to share pia-tun's network namespace
    volumes:                                # (uses tunnel and firewall automatically)
      - ./qbittorrent/config:/config
      - ./qbittorrent/downloads:/downloads

secrets:
  pia_user:
    file: ./secrets/pia_user
  pia_pass:
    file: ./secrets/pia_pass
  ps_user:
    file: ./secrets/ps_user
  ps_pass:
    file: ./secrets/ps_pass
```

See [environment variables](../env.md) for more configuration
