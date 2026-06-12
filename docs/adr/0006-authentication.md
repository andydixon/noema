# ADR-0006: Local authentication design

Date: 2026-09-14  
Status: Accepted

## Context
The admin interface controls a system that can act on infrastructure. It needs strong local authentication first, with room for OIDC.

## Decision
Argon2id (64 MiB, t=3, p=2, 16-byte salt) password hashes; opaque 256-bit session tokens stored as SHA-256 hashes with idle and absolute expiry; cookies `HttpOnly; SameSite=Lax; Path=/` and `Secure` outside development; per-session CSRF token checked on all state-changing cookie-authenticated requests plus Origin check; login limiter per IP (token bucket) and per account (exponential backoff persisted in the database); optional TOTP (RFC 6238, implemented with stdlib HMAC) with hashed recovery codes; session listing and revocation; roles `administrator`, `operator`, `observer` enforced by route middleware. Personal API tokens (hashed, role-bound) authenticate `noemactl` and scripts. `auth.Authenticator` is an interface so OIDC can be added.

## Consequences
- No third-party auth dependencies beyond `x/crypto`.
- First-run bootstrap admin from environment or `noemactl user create`.

## Alternatives considered
- OIDC-only: blocks single-operator deployments.
- JWT sessions: revocation and listing become awkward; opaque tokens are simpler and safer.
