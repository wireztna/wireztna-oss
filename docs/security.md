# Security — WireZTNA CE

> **Status: placeholder.**

## Reporting a vulnerability

Please report security issues privately rather than opening a public issue. Add your
preferred private contact (email or a security policy URL) here before publishing the repo.

## Deployment security notes

- **Set a strong `SECRET_KEY`.** The control plane fails fast on insecure defaults.
- **Plain HTTP by default.** CE serves HTTP over a public IP. For anything beyond testing or a
  trusted network, terminate TLS in front (Caddy, nginx+certbot, or Cloudflare) and set
  `BROKER_PUBLIC_SCHEME=https`.
- **Password login is the default.** Use strong passwords; enable TOTP MFA where available.
  Email OTP and SSO/OIDC are optional and off until configured.
- **Secrets live in `.env`**, which is gitignored. Never commit `.env`, keys, or the database.
- **No committed secrets.** SMTP/broker keys are placeholders; the installer generates real
  secrets with `openssl rand`.
