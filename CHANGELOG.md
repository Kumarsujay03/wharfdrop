# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the
project uses [Semantic Versioning](https://semver.org/).

## [0.6.1] - 2026-10-18

### Fixed
- The transfer speed jumped between a few KB/s and tens of MB/s, and the two progress cards
  showed different numbers. Wi-Fi delivers data in bursts, and the speed was measured over
  0.4-second slices, in two places. Speed is now measured over the last 5 seconds, time left
  over the last 15 seconds, and both cards use the same measurement. On a simulated lossy
  Wi-Fi link, the swing in the shown speed dropped from ±7.4 to ±1.6 MB/s. The actual
  throughput still fills the link.
- A paired device could show "Offline" in the sidebar during a big send. The status check
  timed out on the busy link. Transfer traffic now counts as "online", status checks reuse
  connections, and the header badge updates live.
- An interrupted transfer left an unfinished file on the receiving device. On Windows, this
  also kept the full file size reserved. Unfinished files are now deleted in every case:
  - The sender cancels, stops Wharfdrop, or closes the Wharfdrop window mid-send: deleted
    within about a second (closing the window: within a minute).
  - The receiver stops Wharfdrop: deleted before it exits.
  - Either side crashes, loses power or drops off the network: the receiver deletes it after
    2 minutes without data. If the receiver itself crashed, it deletes it the next time
    Wharfdrop starts (it keeps a list of its unfinished files, and only ever deletes those).
  - A phone closes its page mid-upload: deleted after 2 minutes.
- Stopping cancels sends and downloads in progress right away instead of waiting for them,
  so Wharfdrop exits in about a second.

## [0.6.0] - 2026-10-18

### Fixed
- Disconnecting a phone on the computer left the phone page showing "Connected", and
  disconnecting on the phone left it listed on the computer. Both sides now update within
  about two seconds:
  - **Disconnect** on the phone revokes its key on the computer and removes it at once.
  - Removing a phone, closing the phone link or stopping Wharfdrop sends the phone an
    authenticated, encrypted notice with the reason ("Disconnected", "Phone link closed",
    "updating"). The notice uses the phone's own key, so nobody else can fake or read it.
  - Closing the phone's browser tab marks it offline straight away. Reloading still works.
- Stopping Wharfdrop is now a clean, complete shutdown. It waits briefly for connected phones
  to get the notice, tells paired computers it is going offline, closes every port, and
  only then exits. The web UI shows "Wharfdrop is stopped" after the process has really
  exited, and `wharfdrop stop` waits for it too.

### Added
- Closing the Wharfdrop window stops Wharfdrop after about 10 seconds. Reloading doesn't count,
  other open Wharfdrop windows keep it running, and transfers finish first. You can change this
  in Settings → When this window is closed. `wharfdrop start` and the CLI are not affected.
- If Wharfdrop stops while its window is open, the window says so and reconnects
  by itself when Wharfdrop runs again.

## [0.5.2] - 2026-10-18

### Fixed
- On Windows, the phone QR code could point to the mobile-hotspot adapter (192.168.137.1),
  which Windows keeps active even when the hotspot is off, so phones on the home Wi-Fi could
  not connect. Wharfdrop now picks the address of the network card that carries the default
  route and ranks virtual adapters (mobile hotspot, Hyper-V/WSL, VirtualBox, VMware, Docker,
  VPNs, link-local) last. The top bar shows the same address.

### Added
- The QR dialog shows which address it uses and lets you switch to another address of the
  computer with one click. `wharfdrop phone` lists the other addresses too.

## [0.5.1] - 2026-10-18

### Fixed
- "Can't scan? Type this address on the phone" in the QR dialog did not open: clicks inside
  dialogs were treated as clicks on the backdrop.

### Added
- The QR dialog and `wharfdrop phone` detect an active firewall (ufw or firewalld on Linux,
  Windows Defender Firewall) and show the exact command to allow phones, with a Copy button.
  A blocked phone otherwise just shows a blank, loading page.
- The phone page shows "Connecting to Wharfdrop…" straight away instead of a blank screen.

## [0.5.0] - 2026-10-18

### Added
- **Phones, with no app.** Click **Connect a phone** (or run `wharfdrop phone`) and scan the QR
  code. The phone's browser can send photos, videos and files to the computer, receive files the
  computer sends, and browse shared folders with photo thumbnails and a photo viewer. Live speed
  and time left are shown on the phone too.
- End-to-end encryption for phones: a 256-bit key travels only in the QR code's URL fragment, and
  all traffic is sealed with XChaCha20-Poly1305 (vendored @noble/ciphers on the phone,
  `golang.org/x/crypto` on the computer). Replay protection, single-use download links and
  rate-limiting of failed attempts.
- **Remember this phone**, so it reconnects without scanning while the phone link is open.
- The phone link opens on demand and closes after 10 minutes without activity.
- Phones appear in the sidebar; select one to send it files. `wharfdrop send <phone> <file>`,
  `wharfdrop phone list` and `wharfdrop phone stop` in the terminal, with a QR code drawn in
  the terminal.

### Changed
- Building from source needs Go 1.26 and fetches two small dependencies.

## [0.4.0] - 2026-10-18

### Added
- **Live speed and time left** while sending and receiving, on both devices: in the send panel,
  on the Transfers page and next to Transfers in the sidebar. Finished transfers show their
  average speed.
- **Faster transfers on noisy Wi-Fi.** Files of 24 MB or more are sent as 8 MB chunks over four
  parallel connections, with per-chunk retries. Connections to a device are reused, and both
  sides use larger network buffers. Older devices fall back to a single stream automatically.
- **Cancel** button for sends from the web UI.
- **Profile picture** in Settings, shown to your paired devices.
- **`wharfdrop uninstall`** removes the program, settings, keys and logs (and on Windows the
  Start menu shortcut and PATH entry) while keeping your received files.

### Changed
- Layouts adapt to the window height, so pages fit without scrolling on laptop and desktop
  screens down to 1280×720. On wide screens the Send tab shows recent transfers beside the
  drop area.

### Fixed
- Transfers still in progress were shown as "Done", and the sidebar progress never appeared.
  Running transfers no longer carry a placeholder "finished" time.

## [0.3.0] - 2026-10-18

### Added
- **Previews while browsing.** Click a file to open it in a built-in viewer: photos, videos
  (with seeking), audio, PDFs and text files. Use the arrow keys or buttons to move through
  the folder.
- **Grid and list views** for shared folders, with photo thumbnails made on the sharing device.
- A back button and compact folder path on small screens.
- Banner and screenshots in the README.

### Security
- Previews are served with a sandboxing Content-Security-Policy and `nosniff`. HTML, SVG,
  scripts and other active content are always shown as plain text, so a shared file can never
  run code in the Wharfdrop page.

## [0.2.2] - 2026-10-18

### Changed
- Redesigned web interface: a sidebar of devices, and a page per device with **Files**
  (browse what it shares) and **Send** tabs. Transfers, Shared folders and Settings have
  their own pages. Pairing and confirmations use proper dialogs. A new teal and cyan colour
  palette on neutral greys replaces the old blue. Light and dark themes, and
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

[0.6.1]: https://github.com/Kumarsujay03/wharfdrop/releases/tag/v0.6.1
[0.6.0]: https://github.com/Kumarsujay03/wharfdrop/releases/tag/v0.6.0
[0.5.2]: https://github.com/Kumarsujay03/wharfdrop/releases/tag/v0.5.2
[0.5.1]: https://github.com/Kumarsujay03/wharfdrop/releases/tag/v0.5.1
[0.5.0]: https://github.com/Kumarsujay03/wharfdrop/releases/tag/v0.5.0
[0.4.0]: https://github.com/Kumarsujay03/wharfdrop/releases/tag/v0.4.0
[0.3.0]: https://github.com/Kumarsujay03/wharfdrop/releases/tag/v0.3.0
[0.2.2]: https://github.com/Kumarsujay03/wharfdrop/releases/tag/v0.2.2
[0.2.1]: https://github.com/Kumarsujay03/wharfdrop/releases/tag/v0.2.1
[0.2.0]: https://github.com/Kumarsujay03/wharfdrop/releases/tag/v0.2.0
[0.1.0]: https://github.com/Kumarsujay03/wharfdrop/releases/tag/v0.1.0
