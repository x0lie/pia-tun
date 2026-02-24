# Restarting Brittle Dependents on Port Changes

Most torrent clients support live port updates via API — use `PS_CLIENT` or `PS_SCRIPT` for these (see README). This page covers the rare case where a dependent has no usable API and requires a full container restart when the forwarded port changes (e.g., rtorrent).

## The Problem

`PS_SCRIPT` runs inside the pia-tun container and can do most things — update config files on shared volumes, hit API endpoints, write to files. However, it cannot restart other containers without mounting the Docker socket, which is not recommended for security reasons.

Restarting a Docker container requires Docker socket access. Something outside of pia-tun should do it.

## Options

- **Host-side file watcher** — A script on the host watches `PORT_FILE` (on a shared volume) with `inotifywait` and runs `docker restart` when it changes. Simple and keeps the Docker socket on the host where it belongs.

- **PS_SCRIPT + shared volume** — Use `PS_SCRIPT` to write the new port into the dependent's config file on a shared volume. Combine with a host-side watcher or the dependent's own config reload mechanism if it has one.

- **PS_SCRIPT + webhook** — Use `PS_SCRIPT` to hit a webhook endpoint on a sidecar or host service that has the permissions to restart the dependent.

- **rtorrent.rc scheduler** — rtorrent can periodically read the port from a file using a scheduled command in `rtorrent.rc`. Whether this takes effect without a restart is [debated](https://github.com/crazy-max/docker-rtorrent-rutorrent/discussions/335).

## Recommendation

If possible, switch to a client with API support (qBittorrent, Transmission, Deluge) and use `PS_CLIENT`. These work out of the box with no scripting required.
