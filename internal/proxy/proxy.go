package proxy

import "io"

type Config struct {
	HTTPEnabled   bool
	Socks5Enabled bool
	User          string
	Pass          string
	Socks5Port    int
	HTTPPort      int
}

func transfer(dst io.Writer, src io.Reader) {
	io.Copy(dst, src)
}
