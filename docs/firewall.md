# Kill-Switch Protection

This container implements a strict firewall using iptables-nft or iptables-legacy to prevent any traffic from leaking outside the VPN tunnel — even during startup, reconnects, crashes, and most misconfigurations.
- **Default-deny:** All traffic blocked by default except loopback, VPN interface, and container network(s)
- **Fast protection:** DROP established on all 4 chains after ~25ms (amd64)
- **Auto-detection:** Automatically selects iptables backend by probing for capability (override with `IPT_BACKEND={nft,legacy}`)
- **Optimized rule ordering:** Established/Related connections matched first for maximum performance
- **Firewall persistence:** Rules remain active during reconnections
- **Local network control:** Use `LOCAL_NETWORKS` to allow tunnel bypass to specified CIDRs
- **Temporary exemptions:** Endpoints necessary for setup are exempt from killswitch DROP rules only for time required
- **WAN-check bypass:** NIST Time servers on port 13 are exempt from firewall for determining WAN up/down

## Container Lifecycle
- On startup: Kill-switch immediately applied
- During reconnections: Firewall remains active (zero leak window)
- On normal shutdown: Firewall rules cleanly removed
- On crash/OOM/unexpected-exit: Firewall remains active until network namespace destroyed

## Temporary Exemptions

**Why?**
- WireGuard has two main registration methods - static and dynamic
- PIA is a dynamic registration provider; 
  - pia-tun has to authenticate (get token), fetch server list, and register its WG public key with the chosen server
- Static config providers have you generate or download a .conf file once via their web portal, so exemptions are unnecessary
- Performing the internet-dependent registration process before killswitch setup risks startup leaks, so exemptions are used instead
- Exemptions allow us to set up the killswitch as fast as possible and register securely

**How?**
- Exemptions are specific: the PIA and DNS IPs on tcp ports 443 and 853 respectively
- Exemptions are only used for the duration required (success or after failing for 2 seconds)

**DNS chicken-and-egg:**
- The container needs to resolve DNS to connect to PIA (their ips rotate - cannot hardcode ips), but the killswitch is up
- On the initial connect it makes a temporary exemption to 9.9.9.9:853 (9.9.9.11:853 fallback) for resolving privateinternetaccess.com and serverlist.piaservers.net
- After the initial connection, a caching process runs every 6 hours and caches all relevant ips, pia servers, and a fresh login token to avoid needing resolution
- If the cached endpoints ever fail during a reconnect it will escalate to resolve hostnames again.
- **Critical:** Do NOT set your dependent services to use 9.9.9.9 or 9.9.9.11 on port 853 for the above reason.

## Preventing startup race leaks
- You can use `depends_on: pia-tun: condition: service_healthy` for dependents to wait until killswitch is up.
- This generally is not necessary as pia-tun will win most races (DROP rules applied in ~25ms on amd64), but it does guarantee no startup leaks.
- Not possible in k8s - containers in the same pod will always race.

```yaml
services:
  pia-tun:
    image: x0lie/pia-tun:latest
    container_name: pia-tun
  
  dependent:
    depends_on:
      pia-tun:
        condition: service_healthy
```

## Routing

All traffic is routed through tunnel or dropped, except for `LOCAL_NETWORKS`, the temporary exemptions, and the NIST servers for WAN checks.
- Some routers have DNS redirecting/interception enabled which will grab anything on port 53 and forward the request to their preferred resolver. For this reason it is possible to leak your DNS to the internet if `DNS` and `LOCAL_NETWORKS` overlap.
  - PIA's DNS servers (if enabled) have blackhole routes in the case of fall-through (tunnel down) to avoid this leak vector.
- PIA's internal addresses (PF Gateway, PIA's DNS) are exempt from `LOCAL_NETWORKS` if enabled.
  - An exemption on the relevant 10.x.x.x address will be made and routed through the tunnel regardless of `LOCAL_NETWORKS` overlap.
- You can check out routing with `ip rule list`.

## WAN Outage Handling

pia-tun distinguishes internet outages from VPN failures:
- Tests WAN connectivity via bypass routes
- Exempt routes are only to NIST servers on port 13
  - NIST daytime protocol chosen for reliability and to avoid accidental use
- Waits indefinitely for internet recovery, checking every 5 seconds
- Prevents unnecessary reconnections and log spam during ISP downtime
