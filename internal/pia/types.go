package pia

// Region represents a PIA server region from the server list API.
type region struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	PortForward bool                `json:"port_forward"`
	Servers     map[string][]Server `json:"servers"`
}

// Server is a flattened server entry for the persistent cache.
// Each entry represents one WireGuard server in a specific region.
type Server struct {
	CN         string `json:"cn"`
	IP         string `json:"ip"`
	Region     string `json:"region"`
	RegionName string `json:"region_name"`
	PF         bool   `json:"pf"`
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
