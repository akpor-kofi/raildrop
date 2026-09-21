# Security Policy

## Supported versions

| Version | Supported |
| ------- | --------- |
| 0.1.x   | ✅        |

## Reporting a vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

Use GitHub's private vulnerability reporting:
[Report a vulnerability](https://github.com/akpor-kofi/raildrop/security/advisories/new).

Include as much of the following as you can:

- The type of issue and its impact
- Step-by-step reproduction, ideally with a minimal code sample or test
- Affected version(s) and entry points (`raildrop/server`, `raildrop/client`, …)
- Any known workarounds

## What counts as a security issue in Raildrop

- Session token forgery, replay outside the expiry window, or metadata digest bypass
- Upload key traversal out of the declared namespace (e.g. escaping `public/`)
- Presigned URL leakage beyond the intended expiry or audience
- Private objects becoming publicly reachable through the gateway
- Signature/verification bypass in `readSessionToken`

## Handling of secrets

Raildrop never persists or logs `RAILDROP_SECRET`, bucket credentials, session
tokens, or signed URLs. If you believe you found a case where it does, that is a
security bug — please report it.

## Response

We aim to acknowledge reports within a week and will keep you updated on progress
toward a fix and an advisory credit (unless you prefer to remain anonymous).
