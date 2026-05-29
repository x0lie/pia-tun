# Using PS_SCRIPT

Useful for syncing the forwarded port to a webhook, torrent client API, or to edit a config file. pia-tun has built-in portsyncing to qbittorrent, deluge, and transmission you can see [here](./qbittorrent.md).

## Note about brittle clients

Some clients do not take live updates well - they may require a restart after receiving their new port. Generally I would suggest moving to a more robust client, but you can read more about your options [here](../dependent-restarts.md).

## Example with slskd

```yaml
services:
  pia-tun:
    image: x0lie/pia-tun:latest
    container_name: pia-tun
    cap_add:
      - NET_ADMIN
    cap_drop:
      - ALL
    ports:
      - "5030:5030"
    environment:
      PS_SCRIPT: /script.sh {PORT}          # point to mounted script with {PORT}
    volumes:
      - ./script.sh:/script.sh              # mount script
    secrets:
      - pia_user
      - pia_pass

  slskd:
    image: slskd/slskd:latest
    container_name: slskd
    network_mode: service:pia-tun
    environment:
      SLSKD_REMOTE_CONFIGURATION: "true"    # requirement for receiving config updates via API

secrets:
  pia_user:
    file: ./secrets/pia_user
  pia_pass:
    file: ./secrets/pia_pass
```

**script.sh**:

```sh
#!/bin/sh
set -e

PORT="$1"
SLSKD_URL="http://localhost:5030"
SLSKD_USER="slskd"
SLSKD_PASS="slskd"

TOKEN=$(curl -fsS \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$SLSKD_USER\",\"password\":\"$SLSKD_PASS\"}" \
    "$SLSKD_URL/api/v0/session" \
    | grep -o '"token":"[^"]*"' \
    | cut -d'"' -f4)

curl -fsS -X PATCH \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    -d "{\"soulseek\":{\"listenPort\":$PORT}}" \
    "$SLSKD_URL/api/v0/options"
```

Make sure the script is executable by root:

```bash
sudo chown root:<your-user> script.sh
sudo chmod 760 script.sh
```

## Notes
- Debug the script's output with `LOG_LEVEL=debug`
- It's important that the script exits non-zero when it fails so that pia-tun will retry during transients and startup
- curl with -fsS will keep output clean but informative
- If using cap_drop: ALL (recommended), keep in mind root acts more like a normal user in most cases
  - Apply explicit root ownership to files where applicable
- Special characters in user/pass may give you grief

## Keeping credentials out of the script

```yaml
services:
  pia-tun:
    environment:
      SLSKD_USER_FILE: /run/secrets/slskd_user
      SLSKD_PASS_FILE: /run/secrets/slskd_pass
    secrets:
      - slskd_user
      - slskd_pass

  slskd:
    image: slskd/slskd:latest
    network_mode: service:pia-tun

secrets:
  slskd_user:
    file: ./secrets/slskd_user
  slskd_pass:
    file: ./secrets/slskd_pass
```

The script can read them like this:

```bash
SLSKD_USER=$(cat $SLSKD_USER_FILE)
SLSKD_PASS=$(cat $SLSKD_PASS_FILE)
```
