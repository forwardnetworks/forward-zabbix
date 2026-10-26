"""Settings from the environment only; nothing is read from or written to disk."""
from __future__ import annotations

import os
from dataclasses import dataclass


class ConfigError(Exception):
    pass


@dataclass(frozen=True)
class ZabbixSettings:
    url: str
    token: str | None
    user: str | None
    password: str | None

    @classmethod
    def from_env(cls) -> "ZabbixSettings":
        url = os.environ.get("ZABBIX_URL")
        if not url:
            raise ConfigError("ZABBIX_URL is required (e.g. http://127.0.0.1:8081)")
        token = os.environ.get("ZABBIX_TOKEN")
        user, password = os.environ.get("ZABBIX_USER"), os.environ.get("ZABBIX_PASSWORD")
        if not token and not (user and password):
            raise ConfigError("set ZABBIX_TOKEN, or ZABBIX_USER and ZABBIX_PASSWORD")
        return cls(url=url, token=token, user=user, password=password)


def forward_network_id(cli_value: str | None) -> str:
    value = cli_value or os.environ.get("FORWARD_NETWORK_ID")
    if not value:
        raise ConfigError("pass --network-id or set FORWARD_NETWORK_ID")
    return value
