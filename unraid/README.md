# Unraid

## Install

1. In the Unraid UI, open the **Apps** tab and search for **pia-tun**.
2. Click **Install** and pick a tag: `latest` (newest stable), `1` (stable v1.x, avoids breaking changes), or `develop` (unreleased, may be unstable).
3. Set `PIA_USER` and `PIA_PASS`, set `LOCAL_NETWORKS` to your LAN CIDR (e.g. `192.168.1.0/24`), then **Apply**.

## Routing another container through the VPN (gateway pattern)

pia-tun is a network gateway - not a web app. To send another container's traffic through the tunnel:

1. On the downstream container, set **Network Type** to `Container`, then under **Container Network** select **pia-tun**.
   (This is the UI equivalent of `--network=container:pia-tun`, which you can also pass via Extra Parameters if you prefer.)
2. Publish the ports of downstream containers (e.g. qBittorrent's WebUI at `8080`) on **pia-tun**. Published ports on downstream containers are ignored.
3. Start **pia-tun**, then start the downstream container.
4. Reach the downstream container's WebUI at `http://<unraid-ip>:8080` (note that setting `LOCAL_NETWORKS` is required for accessing from your home networks).

Note that if the pia-tun container is restarted, the downstream containers must be restarted as well.

## Port-syncing

Do the above process, then:

- Set `PS_CLIENT` to `qbittorrent`, `deluge`, or `transmission`
- Set `PS_USER` and `PS_PASS` if necessary (deluge is pass-only, and you can disable auth for localhost in qBittorrent settings to allow pia-tun to connect without a user/pass)
- Set `PS_URL` to http://localhost:{your-port} if using non-standard port (8080 for qBit, 8112 for Deluge, and 9091 for Transmission)

`PS_SCRIPT` is also available if using something other than the three `PS_CLIENT` options

## Notes

- `NET_ADMIN`, `NET_RAW`, and the `tun` device are granted via Extra Parameters (`--cap-drop=ALL --cap-add=NET_ADMIN --cap-add=NET_RAW --device=/dev/net/tun:/dev/net/tun`), already set in the template.
- If the WireGuard kernel module is unavailable, pia-tun will fall back to wireguard-go (via the tun device) automatically.
  - You can see which it's using in the logs here (kernel or userspace):
    ```
    ▶ Establishing connection to amsterdam404...
      ✓ Tunnel configured (kernel)
    ```
- Unlike the Docker Compose examples, the Unraid template passes PIA credentials as masked environment variables rather than secret files.
- Full variable reference: [`docs/env.md`](../docs/env.md). Compose examples: [`docs/compose-examples`](../docs/compose-examples).
