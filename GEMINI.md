# GEMINI.md

## Project Overview

This project, `pia-tun`, aims to be the best-in-class, open-source solution for creating a secure and performant VPN tunnel using WireGuard and Private Internet Access (PIA). It is designed to be run as a Docker container and comes packed with features to enhance security, usability, and performance.

The project is under active development with a strong focus on security, performance, and feature completeness. The author's goal is to create the most downloaded and respected PIA-WireGuard Docker image, competing with established solutions like `thrnz/docker-wireguard-pia` and `qdm12/gluetun`.

### Key Features:

*   **WireGuard:** A modern, fast, and secure VPN protocol.
*   **PIA:** A popular and privacy-focused VPN provider.
*   **Firewall-based Killswitch:** A robust killswitch to prevent traffic from leaking if the VPN connection drops.
*   **SOCKS5 and HTTP Proxy:** Built-in proxies to easily route traffic from other applications through the VPN.
*   **Health Monitoring:** A comprehensive health monitoring system that continuously checks the VPN connection and automatically triggers a reconnection if it fails.
*   **Port Forwarding:** Supports PIA's port forwarding feature, with automatic updates to popular applications like qBittorrent, Deluge, and Transmission.
*   **Performance-Tuned:** The container is optimized for high-throughput, with documented speed tests showing speeds of over 800mbps.
*   **Dockerized and Kubernetes-Ready:** The entire application is packaged as a Docker container for easy deployment and management, with examples for both Docker Compose and Kubernetes.
*   **Multi-Arch Builds:** The project aims to provide multi-arch builds for `arm64` and `armv7` in the future.
*   **Prometheus Metrics:** Exposes a Prometheus endpoint for monitoring the VPN connection and container performance.

## Building and Running

The project is designed to be run as a Docker container. The recommended way to run the application is by using the provided `docker-compose.yml` file. This file defines two services: `pia-tun` and `qbittorrent`. The `pia-tun` service is the main application, and the `qbittorrent` service is an example of an application that can be configured to use the `pia-tun` service for its networking.

To run the application, you will need to have Docker and Docker Compose installed. You will also need to create a `secrets` directory with two files: `pia_user` and `pia_pass`. These files should contain your PIA username and password, respectively.

Once you have created the `secrets` directory, you can run the application using the following command:

```bash
docker-compose up -d
```

This will start the `pia-tun` and `qbittorrent` services in the background. The `qbittorrent` service will be accessible at `http://localhost:8080`, and all of its traffic will be routed through the `pia-tun` service.

For more advanced usage, you can refer to the `notes/docker run command.txt` file, which contains a collection of `docker run` and `podman run` commands with various options and configurations.

## Development Conventions

The project follows a number of development conventions:

*   **Go:** The Go code is formatted using `go fmt`, and it follows the standard Go project layout.
*   **Shell:** The shell scripts are written in `bash` and are formatted using `shfmt`.
*   **Docker:** The Dockerfile is optimized for size and security. It uses a multi-stage build to create a small final image, and it drops all capabilities except for `NET_ADMIN`.
*   **Git:** The project uses Git for version control, and it follows the conventional commit message format.
*   **Security:** The project follows the principle of least privilege, and it is designed to be as secure as possible.

## Future Direction

The author has a clear vision for the future of the project, which includes:

*   Adding support for multi-arch builds (`arm64` and `armv7`).
*   Implementing persistent metrics.
*   Adding support for `DIP_TOKEN`.
*   Improving the UI and log output.
*   Adding more documentation and examples.

The author is also considering creating a version of the project that can be run on a bare-metal Linux server without a container.