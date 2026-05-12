from __future__ import annotations

import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import yaml


@dataclass(frozen=True)
class WatchedGroup:
    jid: str
    name: str = ""


@dataclass(frozen=True)
class BotConfig:
    auto_reply: bool = False
    language: str = "he"
    require_operator_approval_for_sensitive: bool = True
    soul_prompt_path: str = "SOUL.md"
    database_path: str = "data/soul-bot.db"
    bridge_api_url: str = "http://localhost:8080/api"


@dataclass(frozen=True)
class WeeklyWeighInConfig:
    enabled: bool = True
    day: str = "sunday"
    time: str = "08:30"
    timezone: str = "Asia/Jerusalem"
    message: str = "בוקר טוב ❤️ זמן שקילה שבועית. שלחו תמונה חיה עם המשקל שלכם."


@dataclass(frozen=True)
class AppConfig:
    watched_groups: list[WatchedGroup] = field(default_factory=list)
    operators: set[str] = field(default_factory=set)
    bot: BotConfig = field(default_factory=BotConfig)
    weekly_weigh_in: WeeklyWeighInConfig = field(default_factory=WeeklyWeighInConfig)

    @property
    def watched_group_jids(self) -> set[str]:
        return {group.jid for group in self.watched_groups}

    @property
    def operator_ids(self) -> set[str]:
        return {normalize_jid(operator) for operator in self.operators}


def normalize_jid(value: str) -> str:
    value = value.strip()
    if not value:
        return value
    if "@" in value:
        return value
    digits = "".join(ch for ch in value if ch.isdigit())
    return f"{digits}@s.whatsapp.net" if digits else value


def _read_config(path: Path) -> dict[str, Any]:
    if not path.exists():
        return {}
    with path.open("r", encoding="utf-8") as f:
        return yaml.safe_load(f) or {}


def load_config(path: str | None = None) -> AppConfig:
    config_path = Path(path or os.getenv("SOUL_BOT_CONFIG", "config.yaml"))
    raw = _read_config(config_path)

    watched_groups = [
        WatchedGroup(jid=str(group["jid"]), name=str(group.get("name", "")))
        for group in raw.get("watched_groups", [])
        if group.get("jid")
    ]
    operators = {normalize_jid(str(operator)) for operator in raw.get("operators", [])}

    bot_raw = raw.get("bot", {})
    weekly_raw = raw.get("weekly_weigh_in", {})

    bot = BotConfig(
        auto_reply=bool(_env_or_raw("SOUL_BOT_AUTO_REPLY", bot_raw, "auto_reply", False)),
        language=str(_env_or_raw("SOUL_BOT_LANGUAGE", bot_raw, "language", "he")),
        require_operator_approval_for_sensitive=bool(
            _env_or_raw(
                "SOUL_BOT_REQUIRE_OPERATOR_APPROVAL_FOR_SENSITIVE",
                bot_raw,
                "require_operator_approval_for_sensitive",
                True,
            )
        ),
        soul_prompt_path=str(_env_or_raw("SOUL_PROMPT_PATH", bot_raw, "soul_prompt_path", "SOUL.md")),
        database_path=str(_env_or_raw("SOUL_BOT_DB_PATH", bot_raw, "database_path", "data/soul-bot.db")),
        bridge_api_url=str(_env_or_raw("WHATSAPP_API_URL", bot_raw, "bridge_api_url", "http://localhost:8080/api")),
    )
    weekly = WeeklyWeighInConfig(
        enabled=bool(_env_or_raw("SOUL_BOT_WEEKLY_ENABLED", weekly_raw, "enabled", True)),
        day=str(_env_or_raw("SOUL_BOT_WEEKLY_DAY", weekly_raw, "day", "sunday")),
        time=str(_env_or_raw("SOUL_BOT_WEEKLY_TIME", weekly_raw, "time", "08:30")),
        timezone=str(_env_or_raw("SOUL_BOT_TIMEZONE", weekly_raw, "timezone", "Asia/Jerusalem")),
        message=str(_env_or_raw("SOUL_BOT_WEEKLY_MESSAGE", weekly_raw, "message", WeeklyWeighInConfig.message)),
    )
    return AppConfig(watched_groups=watched_groups, operators=operators, bot=bot, weekly_weigh_in=weekly)


def _env_or_raw(env_name: str, raw: dict[str, Any], key: str, default: Any) -> Any:
    value = os.getenv(env_name)
    if value is None:
        return raw.get(key, default)
    if isinstance(default, bool):
        return value.lower() in {"1", "true", "yes", "on"}
    return value
