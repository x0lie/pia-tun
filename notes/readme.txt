Notes for future README.md:

		Document the "LOCAL_NETWORK=all" quirk in README (as you noted), but leave behavior as-is for flexibility.

		if DNS=pia and LOCAL_NETWORK=all, give warning that DNS will fail because pia's dns is a 'local' IP.
		
		The "Run as nonroot" note says it's not useful, but with --cap-drop=ALL --cap-add=NET_ADMIN, it's still valuable—clarify in docs that users should run with those flags.

		## Security Best Practices

		### Secrets Management
		- Use Docker secrets or Kubernetes secrets (not plain environment variables in docker-compose)
		- For Kubernetes: Use SOPS, Sealed Secrets, or External Secrets Operator
		- For Docker: Use `docker secret create` 
		- Rotate PIA credentials periodically

		### Container Security
		- Run container with `--read-only` root filesystem where possible
		- Drop unnecessary capabilities
		- Use `--security-opt=no-new-privileges`




		Nice-to-have Documentation:

		Environment variables reference - Complete list with defaults
		Troubleshooting section - Common issues
		Port forwarding setup guide - It's complex, users will need help
		Examples directory - docker-compose examples for common setups (qBittorrent, Transmission, etc.)


		## Security

		This container follows the **principle of least privilege**:
		- Requires only `NET_ADMIN` capability (minimum for VPN functionality)
		- No other capabilities needed
		- Never requires `--privileged` mode
		- Drops all unnecessary capabilities

		This is the most secure configuration possible for a VPN container.






Log displays IP and Port
Need these ENV variables
	PIA_USER
	PIA_PASS
	PIA_LOCATION
Optional (recommended) ENV variables
	PORT_FORWARDING
	MTU
	LOCAL_NETWORK
	DNS
Need these sysctls
	net.ipv4.conf.all.src_valid_mark=1
Optional (recommended) sysctls
	net.ipv4.tcp_rmem="4096 87380 26214400"
	net.ipv4.tcp_wmem="4096 65536 26214400"
Need these cap add
	NET_ADMIN
	NET_RAW
Recommended cap drop
	ALL

		# INCOMPATIBLE with user set UID:GID



wireguard-pia best names:
pia-tun					piatun
pia-guard				piaguard
wireguard-pia-plus		wireguard-pia-stack

Best speedtest:

curl -o /dev/null -w "%{speed_download} bytes/sec\n" http://ipv4.download.thinkbroadband.com/100MB.zip

consider adding this:
	sysctls: net.ipv4.tcp_congestion_control: bbr



Cilium Minor Polish (Once VM Fixed):Your config's great, but add for UDP/VPN: 
	Edit values ConfigMap → bpf.mapDynamicSizeRatio: "0.75" (larger eBPF maps for bursts). 
	Reconcile: flux reconcile helmrelease cilium -n flux-system.
	Enable Bandwidth Manager: Add enableBandwidthManager: true (throttles fairly, boosts latency-sensitive VPN). 
	



curl -X POST "http://radarr.media.svc.cluster.local:7878/api/v3/release/push?apikey=b3fbf8cb41bc446ba378898400b63767" \
-H "Content-Type: application/json" \
-d '{
  "title": "Catch-22 1970 1080p BluRay x264-PiGNUS",
  "quality": {"quality": {"id": 18, "name": "Bluray-1080p", "source": "bluray", "resolution": 1080, "modifier": "remux"}},
  "customFormats": [],
  "customFormatScores": [],
  "languages": [{"id": 1, "name": "English"}],
  "releaseHash": "",
  "releaseTitle": "Catch-22 1970 1080p BluRay x264-PiGNUS",
  "size": 12345678,
  "indexer": "IPTorrents",
  "indexerId": 0,
  "downloadUrl": "https://iptorrents.com/download.php/6962272/Catch-22+1970+1080p+BluRay+x264-PiGNUS.torrent?torrent_pass=yourpass",
  "link": "https://iptorrents.com/torrent.php?id=6962272",
  "publishDate": "2025-10-22T19:33:00Z",
  "commentUrl": ""
}'


export TORRENT_NAME="Love in the Clouds S01E14 1080p NF WEB-DL AAC2 0 H 264-BiOMA"
export SIZE=400000000  # Guess ~380MB; adjust if logs show exact
export INDEXER_NAME="IPTorrents"
export DOWNLOAD_URL="https://iptorrents.com/download.php?.../Love+in+the+Clouds..."  # From your feed
export INFO_URL="https://iptorrents.com/details.php?id=..."
export PROTOCOL="torrent"
/scripts/arr-filter.sh "http://radarr.media.svc.cluster.local:7878/api/v3/release/push?apikey=b3fbf8cb41bc446ba378898400b63767"

export TORRENT_NAME="Watching You S01E05 720p STAN WEB-DL DDP5 1 Atmos H 264-RAWR"
export SIZE=1500000000
export INDEXER_NAME="IPTorrents"
export DOWNLOAD_URL="https://iptorrents.com/download.php?id=YOUR_REAL_ID_HERE/Watching+You+S01E05+720p+STAN+WEB-DL+DDP5.1+Atmos+H.264-RAWR.torrent?torrent_pass=yourpass"  # Real!
export INFO_URL="https://iptorrents.com/details.php?id=YOUR_REAL_ID_HERE"
export PROTOCOL="torrent"
/scripts/arr-filter.sh "http://sonarr.media.svc.cluster.local:8989/api/v3/release/push?apikey=33441aa76c5f41169993e69eed0731a6"