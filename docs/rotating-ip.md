# PIA's rotating IP (resolved)

In mid-2026, PIA's new `Server-*` infra rotated the outbound IP on nearly every request, which broke private tracker rules on simultaneous IPs.

Kind of unfortunate because the tech is actually pretty neat and obfuscates the user better, but for private trackers it wasn't going to work.

PIA has since reversed their position on this, saying it's 'fixed' [here](https://www.reddit.com/r/PrivateInternetAccess/comments/1uv6aeh/comment/p17pzsu/).

In other words, connections are as they were before the change - stable outbound IPs, compliant with private tracker rules.

If you want to verify your own connection, run this a few times and the IP should not change:
```bash
docker exec pia-tun curl -s ifconfig.me
```
