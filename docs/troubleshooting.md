# Troubleshooting

Common issues and solutions for pia-tun, particularly on older or minimal host systems.

## Kernel Module Issues

### Symptoms
- Container fails to start with iptables errors
- Many IPv6-related errors like `ip6tables: No chain/target/match by that name` during startup
- Connection tracking errors

### Cause
Some minimal systems (Synology NAS, older distros, minimal VPS) don't load firewall-related kernel modules until something needs them. Containers can't load kernel modules themselves.

### Solution
Load the required modules on the **host** system:

```bash
sudo modprobe ip_tables
sudo modprobe ip6_tables
sudo modprobe ip6table_filter
sudo modprobe nf_conntrack
```

To make this permanent across reboots, add to `/etc/modules-load.d/pia-tun.conf`:

```
ip_tables
ip6_tables
ip6table_filter
nf_conntrack
```

---

## VPN Connectivity Drops After Setup + Infinite Restarts

### Symptoms
- Container starts successfully
- WireGuard interface is created
- Handshakes fail or connectivity drops immediately after connecting - Verifying Connection fails

### Cause
Strict Reverse Path Filtering (`rp_filter=1`) drops VPN return traffic because packets arrive on a different interface than the kernel expects.

This commonly affects:
- Older distros
- Some NAS systems

### Solution
On the **host** system, set loose mode for reverse path filtering:

```bash

sudo sysctl -w net.ipv4.conf.all.rp_filter=2
sudo sysctl -w net.ipv4.conf.default.rp_filter=2
```

Many modern systems use these by default.
To make permanent, add to `/etc/sysctl.d/99-pia-tun.conf`:

```
net.ipv4.conf.all.rp_filter=2
net.ipv4.conf.default.rp_filter=2
```

Then apply: `sudo sysctl --system`

**Note:** `rp_filter=2` (loose mode) still provides protection against IP spoofing while allowing VPN traffic. This is preferred over `rp_filter=0` (disabled).

---

## iptables-legacy Issues

### Symptoms
- Errors in log about iptables "Permission denied"

### Solutions

**If nftables is not an option:**
The container will test for nftables capability and fallback to iptables-legacy (x_tables) if it fails. If your host has both nftables and iptables, it will prefer iptables for reliability if docker writes its rules to iptables.

```yaml
cap_add:
  - NET_ADMIN
  - NET_RAW     # Typically a requirement for iptables (legacy). Rarely required for iptables-nft/nftables backend.
cap_drop:
  - ALL
```

**To force a specific IPT backend:**
```yaml
environment:
  - IPT_BACKEND=nftables  # Force nf_tables (iptables-nft)
  - IPT_BACKEND=legacy    # Force x_tables (iptables-legacy)
```

## WireGuard Issues

### Symptoms
- "Kernel WireGuard unavailable" warning
- WireGuard interface fails to create entirely
- Handshake timeouts

### Solutions

**If kernel WireGuard is unavailable:**
The container automatically falls back to `wireguard-go` (userspace) if wireguard kernel method fails. This works fine but uses slightly more CPU. It also may drastically affect your speeds. To use kernel WireGuard:

```bash
# On host system
sudo modprobe wireguard
```

**To force a specific backend:**
```yaml
environment:
  - WG_BACKEND=userspace  # Force wireguard-go
  - WG_BACKEND=kernel     # Force kernel (fails if unavailable)
```

**For TUN device issues with wireguard-go:**
```yaml
devices:
  - /dev/net/tun:/dev/net/tun
```
