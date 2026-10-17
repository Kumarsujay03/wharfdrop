# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the
project uses [Semantic Versioning](https://semver.org/).

## [0.1.0] - 2026-10-18

### Added
- LAN discovery over UDP multicast, working across 2.4/5/6 GHz bands of one router.
- Bluetooth-style pairing with a 6-digit code shown on both devices.
- TLS 1.3 with mutual certificate pinning for every connection after pairing.
- Send files and folders to a paired device's incoming folder.
- Read-only folder shares, per device or for all paired devices; browse and download, folders as zip.
- Web UI (drag and drop) on 127.0.0.1 and a full command-line interface.
- Release binaries for Linux, Windows and macOS on amd64 and arm64.

[0.1.0]: https://github.com/Kumarsujay03/wharfdrop/releases/tag/v0.1.0
