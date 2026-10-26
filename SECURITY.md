# Security Policy

This is an experimental Forward field integration, not a Forward product. It handles credentials for Forward
(an API token) and Zabbix (an API token or login), and a webhook password.

## Reporting a vulnerability

Report privately; do not open a public issue, pull request or discussion for a suspected vulnerability.

- Use **GitHub Private Vulnerability Reporting** on this repository (Security tab, Report a vulnerability), or
- email **security@forwardnetworks.com** with the subject `forward-zabbix security report`.

Please do not include credentials, customer topology or device data in a report.

## Scope

The code and deployment files in this repository: the CLI, the Go API, the webhook receiver, the Zabbix
templates, the systemd units and the Dockerfile. Keep the receiver behind TLS; see
[Run as a service](docs/service.md).
