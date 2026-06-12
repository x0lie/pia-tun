# PIA's new Rotating IP architecture

Instead of the classic infra where you have a single public IP, PIA is moving to IP obfuscation/rotation:
- A large group of IPs (~25? maybe more) for both egress and ingress, used at random
- Port forwarding itself seems to work just fine - the port is forwarded on each of these IPs

## The issue (private tracker rules)

Most private trackers have rules around simultaneous IP usage:
- Max 1-3 active IPs per account (or similar)

A different IP on each announcement to the tracker will look like account sharing, spam, or other abuse.

It doesn't seem to be a problem for public trackers and most other things - functionally it works just fine and is actually kind of neat, aside from this issue.

## Solutions

There are really only two solutions:
- Subscribe to PIA's Dedicated IP
  - (Make sure to choose a location that supports port forwarding if you do this)
- Use a specific server still on the old infra like this:
  ```yaml
  env:
    - PIA_CN=toronto401
    - PIA_IP=66.56.80.165
  ```
  - Presumably these will disappear eventually

It may also be possible for the trackers to accommodate this, but they are very opaque groups that are hard to speak with.

It's an unfortunate change - I'm not sure if PIA just overlooked this scenario or if they think it's worthwhile regardless of the pains for those with private trackers.

In my opinion they should give the user a choice between a stable/dynamic IP - Dedicated IP is not a 1:1 solution with the previous architecture regardless of the additional cost.

If you feel so inclined, make a reddit post or talk to their support about the issue. If you do, I would be specific - it breaks private tracker rules, and Dedicated IP will be the only real option once they complete their transition.

## How to identify the different architectures

The most direct way is just to send this a few times to see if the IP is static or dynamic. It changes with basically every request on the new infra:
```bash
docker exec pia-tun curl -s ifconfig.me
```

The common names seem to have changed with the new infra as well:
- ontario401 (old) vs Server-12706-0a (new)

You can see the remaining old infra (port-forwarding capable, WireGuard, discluding "Server-*" common names) with this command:
```bash
curl -s https://serverlist.piaservers.net/vpninfo/servers/v6 | head -1 | jq -c '.regions[] | select(.port_forward == true) | .id as $r | (.servers.wg[]? | select(.cn | startswith("Server-") | not) | {region: $r, cn, ip})'
```
- This list rotates - they don't show all of their endpoints at once

Some known good endpoints in ontario/toronto:
```
ontario401   66.56.80.11
ontario401   66.56.80.22
toronto401   66.56.80.165
toronto402   66.56.80.188
toronto403   66.56.80.227
toronto405   66.56.81.52
toronto415   191.96.36.55
toronto418   191.96.36.149
toronto425   179.61.197.105
toronto433   212.32.48.137
toronto434   212.32.48.98
```
How long they'll remain up is unknown.
