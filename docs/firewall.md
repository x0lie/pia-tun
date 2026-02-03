# Kill-Switch Protection

This container implements a strict firewall using iptables-nft or -legacy to prevent any traffic from leaking outside the VPN tunnel — even during startup, reconnects, crashes, or misconfigurations.
- **Zero leak windows**: DROP rule is established immediately and never removed
- **Optimized rule ordering**: Established/Related connections matched first for maximum performance
- **Auto-detection**: Automatically selects iptables backend by testing for conflicts (override with `IPT_BACKEND`)
- **Default-deny**: All traffic blocked except loopback and VPN interface
- **Firewall persistence**: Rules remain active during reconnections
- **Local network control**: LAN access requires explicit `LOCAL_NETWORKS` configuration
- **Bypass routing**: WAN health checks use policy routing (no firewall exemptions) to NIST Time servers on port 13

**Container Lifecycle:**
- On startup: Kill-switch immediately applied
- During reconnections: Firewall remains active (zero leak window)
- On normal shutdown: Firewall rules cleanly removed after dependents stop
- On crash/OOM: Firewall remains active until network namespace destroyed

**Dependent Services:**

For containers sharing the network namespace (`network_mode: "service:pia-tun"`), use Docker Compose healthchecks to ensure the kill-switch is active before dependent services start. See the [Coordinating with Dependent Services](#coordinating-with-dependent-services) section for detailed examples and best practices.

**Important:** As long as you use `depends_on` with the killswitch healthcheck, you will never have a leak occur during startup.

**For the Paranoid:** You can use depends_on: with this healthcheck and condition: service_healthy for dependents to wait until killswitch is up:

```yaml
services:
  pia-tun:
    image: x0lie/pia-tun:latest
    container_name: pia-tun
    healthcheck:
      test: ["CMD", "test", "-f", "/tmp/killswitch_up"]
      interval: 5s
      timeout: 2s
      retries: 3
      start_period: 3s
  
  dependent:
    depends_on:
      pia-tun:
        condition: service_healthy


secrets:
  pia_user:
    file: ./secrets/pia_user
  pia_pass:
    file: ./secrets/pia_pass
```

**DNS chicken and egg:**
- The container needs to resolve DNS to connect to PIA (their ips rotate - cannot hardcode ips)
- On the initial connect it uses 9.9.9.9:853 (9.9.9.11:853 fallback) to resolve www.privateinternetaccess.com and serverlist.piaservers.net
- A temporary exemption is made through the firewall for 9.9.9.9 port 853 tcp specifically, and then removed upon resolution or failure after 2 seconds
- After the initial connection, a caching process runs every 6 hours and caches all relevant ips, pia servers, and a fresh login token
- If the cached endpoints ever fail it will escalate to resolve hostnames again.

**Critical:** Do NOT set your dependent services to use 9.9.9.9 or 9.9.9.11 on port 853 for the above reason.

## WAN Outage Handling

The health monitor distinguishes internet outages from VPN failures:
- Tests WAN connectivity via bypass routing
- Bypass routes are only to NIST servers on port 13
- Waits indefinitely for internet recovery with exponential backoff (5s → 160s)
- Prevents unnecessary reconnections and log spam during ISP downtime

**Privacy note on NIST bypass**: These checks query NIST's public Internet Time Service (time.nist.gov) via the anonymous Daytime Protocol on port 13. NIST operates under strict U.S. federal privacy guidelines and **strives to collect no personal information** for such public, unauthenticated queries — logging is limited to aggregate, non-identifying operational data (e.g., for server management and abuse detection) per their site privacy policy. This approach minimizes real IP exposure compared to third-party commercial probes or DNS resolvers.