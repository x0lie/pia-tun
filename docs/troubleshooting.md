# Troubleshooting

Common issues and solutions for pia-tun, particularly on older or minimal host systems.

## Read the logs

The logs are made to be very useful

```bash
docker logs pia-tun
```

`LOG_LEVEL=debug` and `LOG_LEVEL=trace` may also be useful if no explicit errors at info level

## Verify VPN is Working

Check your IP is different from your real IP:
```bash
docker exec pia-tun curl -s ifconfig.me
```

Verify it reconnects on failures:
```bash
docker exec pia-tun ip link delete pia0 && docker logs pia-tun -f
```

## Finding Available PIA Locations

Query the PIA server list directly:
```bash
curl -s 'https://serverlist.piaservers.net/vpninfo/servers/v6' | head -n -1 | jq -r '.regions[].id' | sort
```

- Alternatively, use `PIA_LOCATIONS=all` (default) with `LOG_LEVEL=debug` to see the lowest latency options
- You can also input an invalid option for PIA_LOCATIONS to see all locations

## Common Issues

**Port Syncer not reaching client:**
- Verify `PS_URL` is accessible from pia-tun container
- Check `PS_USER` and `PS_PASS` or equivalent secrets are correct
- Review `LOG_LEVEL=debug` logs for API communication errors

**Cannot access metrics, proxy, or dependent webui from LAN:**
- Services are localhost and container network only by default for security
- Use `LOCAL_NETWORKS=192.168.1.0/24` or whatever networks necessary
- Be sure to publish the relevant ports to pia-tun service in docker-compose.yml:

```yaml
services:
  pia-tun:
    environment:
      - PROXY_ENABLED=true
    ports:
      - 1080:1080   # SOCKS5
      - 8888:8888   # HTTP Proxy
      - 9090:9090   # Metrics
```

## iptables Issues

### Symptoms
- Container fails to start with iptables errors

### Cause
- Some minimal systems (Synology NAS, older distros, minimal VPS) don't load firewall-related kernel modules until something needs them. Containers can't load kernel modules themselves.
- Most modern kernels (Ubuntu 20.04+, Debian 10+, etc.), use nf_tables (modern iptables backend) and are unlikely to have issues.

### How to tell which backend in use
You can see what backend you're using in the container logs at `LOG_LEVEL=info` (default) near the top:
```bash
▶ Applying killswitch...
  ✓ Baseline established (iptables-legacy)
```

### Solution
Load the required modules on the **host** system (iptables-legacy only):

```bash
sudo modprobe ip_tables
sudo modprobe ip6_tables
sudo modprobe ip6table_filter
```

To make this permanent across reboots (not the same for all distros), add to `/etc/modules-load.d/pia-tun.conf`:

```
ip_tables
ip6_tables
ip6table_filter
```

These kernel modules are requirements for iptables-legacy. If you don't have them and can't get them (and can't use iptables-nft), [make an issue](https://github.com/x0lie/pia-tun/issues)

**To force a specific IPT backend:**
```yaml
environment:
  - IPT_BACKEND=nft       # Force nf_tables (iptables-nft)
  - IPT_BACKEND=legacy    # Force x_tables (iptables-legacy)
```

## WireGuard Issues

### Symptoms
- "Kernel WireGuard unavailable" warning
- WireGuard interface fails to create entirely

### Solutions

**If kernel WireGuard is unavailable:**
The container automatically falls back to `wireguard-go` (userspace) if wireguard kernel method fails. This works fine but uses more CPU and may negatively affect your speeds. To use kernel WireGuard:

```bash
# On host system
sudo modprobe wireguard
```

**To force a specific backend:**
```yaml
environment:
  - WG_BACKEND=userspace  # Force wireguard-go
  - WG_BACKEND=kernel     # Force kernel
```
