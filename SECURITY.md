# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in edr, please report it responsibly.

**Report via [GitHub Security Advisories](https://github.com/jordw/edr/security/advisories/new).**

Please include:
- Description of the vulnerability
- Steps to reproduce
- Potential impact

I will acknowledge receipt within 48 hours and aim to provide a fix or mitigation within 7 days for critical issues.

## Scope

edr runs locally and does not make network requests. Security concerns are most likely to involve:
- Arbitrary file read/write outside the target repository
- Parser or index crashes via crafted source files, file names, or symbol names
- Path traversal in command arguments

## Supported Versions

Only the latest release is supported with security updates.
