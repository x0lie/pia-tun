# Simple Traefik + pia-tun example

## Key Point

- Traefik labels must be on pia-tun, not qbittorrent
- This is because pia-tun shares its network namespace with qbittorrent
- You can include multiple dependents' labels - qBit + Sab, for example

## Ports

- Note that pia-tun does not need a ports section when using a reverse-proxy
- Connections flow through traefik's published ports instead, then to qBit via pia-tun's labels and network namespace

```yaml
networks:
  traefik:
    driver: bridge

services:
  traefik:
    image: traefik:latest
    container_name: traefik
    networks:
      - traefik
    ports:
      - "80:80"
      - "443:443"
    command:
      - --providers.docker=true
      - --providers.docker.exposedbydefault=false
      - --entrypoints.web.address=:80
      - --entrypoints.websecure.address=:443

  pia-tun:
    image: x0lie/pia-tun:latest
    container_name: pia-tun
    cap_add:
      - NET_ADMIN
    cap_drop:
      - ALL
    networks:
      - traefik
    environment:
      - PIA_LOCATIONS=ca_ontario,ca_ontario-so,ca_toronto
      - PS_CLIENT=qbittorrent
    secrets:
      - pia_user
      - pia_pass
      - ps_user
      - ps_pass
    labels:
      - traefik.docker.network=traefik
      - traefik.http.routers.qbittorrent-rtr.tls=true
      - traefik.http.routers.qbittorrent-rtr.entrypoints=websecure
      - traefik.http.routers.qbittorrent-rtr.service=qbittorrent-svc
      - traefik.http.services.qbittorrent-svc.loadbalancer.server.port=8080
      - traefik.http.routers.qbittorrent-rtr.rule=Host(`qbit.mydomain.com`)

  qbittorrent:
    image: lscr.io/linuxserver/qbittorrent:latest
    container_name: qbittorrent
    network_mode: "service:pia-tun"
    restart: unless-stopped
    volumes:
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
