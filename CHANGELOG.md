# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the
project uses [Semantic Versioning](https://semver.org/).

## [0.2.2] - 2026-10-18

### Changed
- Redesigned web interface: a sidebar of devices, and a page per device with **Files**
  (browse what it shares) and **Send** tabs. Transfers, Shared folders and Settings have
  their own pages. Pairing and confirmations use proper dialogs. Light and dark themes, and
  a layout that works on phones.
- Long file names no longer push the page wider than the window.
- Files can be dropped anywhere on the window to send them to the selected device.

### Added
- **Open folder** buttons that open the received-files folder in your file manager.
- Settings page for the device name, the received-files folder and update mode.

### Fixed
- `install.ps1` could not read GitHub's "latest release" redirect on Windows PowerShell 5.1,
  so it fell back to the GitHub REST API. That fails once the 60-requests-per-hour limit is
  hit and can return a stale version. It now reads the redirect directly on 5.1 and 7.
- Choosing a device in the old Browse drop-down did nothing when only one device was paired.

## [0.2.1] - 2026-10-18

### Fixed
- Double-clicking `wharfdrop.exe` on Windows only flashed a console window. It now starts
  Wharfdrop in the background and opens the web UI. Errors stay on screen until you press Enter.

### Added
- One-line installers: `install.ps1` for Windows (PATH, Start menu shortcut, uninstall) and
  `install.sh` for Linux and macOS. Both verify the release checksum.
- `build.ps1` to build on Windows without `make`.
- CI smoke tests that run the Windows binary and the Windows installer.

## [0.2.0] - 2026-10-18

### Added
- Automatic updates from GitHub Releases, verified with an Ed25519 signature compiled into
  the app and a SHA-256 checksum. Updates install while idle and Wharfdrop restarts itself.
- `auto-update` setting (`auto`, `notify`, `off`) in the web UI and `wharfdrop set`.
- `wharfdrop update [--check]` and `wharfdrop version` commands.
- Update status in `wharfdrop status` and in the web UI header.
- `wharfdrop start` runs Wharfdrop in the background and `wharfdrop stop [--force]` stops it
  completely. `wharfdrop ui` starts it if needed. A **Stop** button is in the web UI header.
  Wharfdrop never starts on its own at login.

### Changed
- Release checksums are now signed (`SHA256SUMS.sig`).
- Config is only rewritten when a peer's address changes, not on every request.
- Pending pairing requests are capped to stop a noisy device flooding the screen.

## [0.1.0] - 2026-10-18

### Added
- LAN discovery over UDP multicast, working across 2.4/5/6 GHz bands of one router.
- Bluetooth-style pairing with a 6-digit code shown on both devices.
- TLS 1.3 with mutual certificate pinning for every connection after pairing.
- Send files and folders to a paired device's incoming folder.
- Read-only folder shares, per device or for all paired devices; browse and download, folders as zip.
- Web UI (drag and drop) on 127.0.0.1 and a full command-line interface.
- Release binaries for Linux, Windows and macOS on amd64 and arm64.

[0.2.2]: https://github.com/Kumarsujay03/wharfdrop/releases/tag/v0.2.2
[0.2.1]: https://github.com/Kumarsujay03/wharfdrop/releases/tag/v0.2.1
[0.2.0]: https://github.com/Kumarsujay03/wharfdrop/releases/tag/v0.2.0
[0.1.0]: https://github.com/Kumarsujay03/wharfdrop/releases/tag/v0.1.0
