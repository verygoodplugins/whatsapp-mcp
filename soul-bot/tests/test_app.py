from pathlib import Path

from fastapi.testclient import TestClient

from soul_bot.app import WebhookPayload, create_app, extract_weight
from soul_bot.config import AppConfig, BotConfig, WatchedGroup, claude_code_model_args
from soul_bot.db import BotStore


def test_extract_weight_from_hebrew_message():
    assert extract_weight("היום אני 82.4 קילו") == 82.4


def test_claude_code_model_args_are_optional():
    assert claude_code_model_args(BotConfig()) == []
    assert claude_code_model_args(BotConfig(claude_code_model="claude-opus-4-6")) == ["--model", "claude-opus-4-6"]


def test_webhook_ignores_unwatched_group(tmp_path: Path):
    app = create_app(
        AppConfig(watched_groups=[WatchedGroup(jid="group@g.us")], bot=BotConfig(database_path=str(tmp_path / "bot.db"))),
        BotStore(str(tmp_path / "bot.db")),
    )

    response = TestClient(app).post(
        "/whatsapp/webhook",
        json={"sender": "972500000000@s.whatsapp.net", "chatJID": "other@g.us", "content": "hello"},
    )

    assert response.status_code == 200
    assert response.json()["ignored"] is True


def test_webhook_stores_participant_and_weight(tmp_path: Path):
    store = BotStore(str(tmp_path / "bot.db"))
    app = create_app(
        AppConfig(watched_groups=[WatchedGroup(jid="group@g.us")], bot=BotConfig(database_path=str(tmp_path / "bot.db"))),
        store,
    )

    response = TestClient(app).post(
        "/whatsapp/webhook",
        json={"sender": "972500000000@s.whatsapp.net", "chatJID": "group@g.us", "content": "משקל 82.4 קג"},
    )

    assert response.status_code == 200
    data = response.json()
    assert data["role"] == "participant"
    assert data["weight_kg"] == 82.4
    assert store.list_members("group@g.us")[0]["role"] == "participant"


def test_image_payload_is_saved(tmp_path: Path):
    store = BotStore(str(tmp_path / "bot.db"))
    config = AppConfig(
        watched_groups=[WatchedGroup(jid="group@g.us")],
        bot=BotConfig(database_path=str(tmp_path / "bot.db"), media_dir=str(tmp_path / "media")),
    )
    app = create_app(config, store)

    response = TestClient(app).post(
        "/whatsapp/webhook",
        json={
            "sender": "972500000000@s.whatsapp.net",
            "chatJID": "group@g.us",
            "content": "",
            "mediaType": "image",
            "mediaFilename": "scale.jpg",
            "mediaBase64": "aGVsbG8=",
        },
    )

    data = response.json()
    assert response.status_code == 200
    assert data["media_path"]
    assert Path(data["media_path"]).read_bytes() == b"hello"
