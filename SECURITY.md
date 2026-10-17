# Security Policy

## Reporting a vulnerability

Please **do not** open a public issue for security problems.

Report it privately through GitHub instead:

1. Open the repository's **Security** tab.
2. Click **Report a vulnerability**.
3. Describe the issue, how to reproduce it, and what impact it has.

You'll get a response as soon as possible, usually within a few days. Once the issue is
confirmed, a fix will be prepared and you'll be credited in the advisory unless you'd
prefer not to be.

## Supported versions

Only the latest release is supported with security fixes.

## Threat model

Wharfdrop is designed for devices you own on a network you mostly trust, such as home Wi-Fi.

**In scope**

- Another device on the same network reading or modifying transfers (prevented by TLS 1.3
  with pinned certificates).
- An active man-in-the-middle during pairing (detected by the 6-digit code differing on
  the two screens, provided users compare it).
- A paired device reading outside a folder that was shared with it (paths are confined to
  the share root).
- Other local processes or web pages driving the daemon (the loopback API needs a random
  per-install token).

**Out of scope**

- A paired device that is itself compromised. Pairing means trust: a paired device can send
  you files and read what you shared with it.
- Malware on the same machine running as your user. It can read the private key and token.
- Users who accept a pairing without comparing the code.
- Denial of service from devices on the same LAN.
