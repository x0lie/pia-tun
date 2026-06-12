# Unraid template

A [Community Applications](https://forums.unraid.net/topic/38582-plug-in-community-applications/) template for running pia-tun on Unraid.

## Install

### From the template URL (now)

1. In the Unraid UI, go to **Docker → Add Container**.
2. In the **Template** field at the top, paste:
   ```
   https://raw.githubusercontent.com/x0lie/pia-tun/main/unraid/pia-tun.xml
   ```
3. Set `PIA_USER` and `PIA_PASS`, set `LOCAL_NETWORKS` to your LAN CIDR (e.g. `192.168.1.0/24`), then **Apply**.

### From Community Applications (once listed)

Search for **pia-tun** in the **Apps** tab.

## Routing another container through the VPN (gateway pattern)

pia-tun is a network gateway, not a web app. To send another container's traffic through the tunnel:

1. Start **pia-tun** first.
2. On the downstream container (e.g. qBittorrent), set **Network Type** to `Container`, then under **Container Network** select **pia-tun**.
   (This is the UI equivalent of `--network=container:pia-tun`, which you can also pass via Extra Parameters if you prefer.)
3. Remove that container's own port mappings and publish those ports **on pia-tun** instead (the template ships a qBittorrent WebUI `8080` example). Reach the downstream WebUI at `http://<unraid-ip>:8080`.

## Notes

- `NET_ADMIN` is granted via Extra Parameters (`--cap-drop=ALL --cap-add=NET_ADMIN`), already set in the template.
- Unraid does not load the WireGuard kernel module for containers by default, so pia-tun automatically uses the userspace backend (wireguard-go). No host configuration needed.
- Unlike the Docker Compose examples, the Unraid template passes PIA credentials as masked environment variables rather than secret files.
- Full variable reference: [`docs/env.md`](../docs/env.md). Compose examples: [`docs/compose-examples`](../docs/compose-examples).
