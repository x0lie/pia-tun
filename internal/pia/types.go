package pia

// Region represents a PIA server region from the server list API.
type Region struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	PortForward bool                `json:"port_forward"`
	Servers     map[string][]Server `json:"servers"`
}

// Server represents a single PIA server within a region.
type Server struct {
	IP string `json:"ip"`
	CN string `json:"cn"`
}

// CachedServer is a flattened server entry for the persistent cache.
// Each entry represents one WireGuard server in a specific region.
type CachedServer struct {
	CN         string `json:"cn"`
	IP         string `json:"ip"`
	Region     string `json:"region"`
	RegionName string `json:"region_name"`
	PF         bool   `json:"pf"`
}

// FlattenRegions extracts WireGuard servers from regions into a flat CachedServer list.
func FlattenRegions(regions []Region) []CachedServer {
	var servers []CachedServer
	for _, r := range regions {
		for _, srv := range r.Servers["wg"] {
			servers = append(servers, CachedServer{
				CN:         srv.CN,
				IP:         srv.IP,
				Region:     r.ID,
				RegionName: r.Name,
				PF:         r.PortForward,
			})
		}
	}
	return servers
}

// AddKeyResponse contains the WireGuard tunnel parameters returned by
// PIA's addKey API after registering a client public key.
type AddKeyResponse struct {
	Status     string   `json:"status"`
	ServerKey  string   `json:"server_key"`
	ServerPort int      `json:"server_port"`
	PeerIP     string   `json:"peer_ip"`
	ServerVIP  string   `json:"server_vip"`
	DNSServers []string `json:"dns_servers"`
}
