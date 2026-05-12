from __future__ import annotations

import sqlite3
from dataclasses import dataclass
from pathlib import Path
from typing import Any


@dataclass(frozen=True)
class StoredMessage:
    id: int
    chat_jid: str
    sender_jid: str
    role: str
    content: str
    media_type: str | None
    created_at: str


class BotStore:
    def __init__(self, path: str):
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self._init_schema()

    def connect(self) -> sqlite3.Connection:
        conn = sqlite3.connect(self.path)
        conn.row_factory = sqlite3.Row
        return conn

    def _init_schema(self) -> None:
        with self.connect() as conn:
            conn.executescript(
                """
                CREATE TABLE IF NOT EXISTS groups (
                    jid TEXT PRIMARY KEY,
                    name TEXT NOT NULL DEFAULT '',
                    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
                );

                CREATE TABLE IF NOT EXISTS members (
                    chat_jid TEXT NOT NULL,
                    member_jid TEXT NOT NULL,
                    role TEXT NOT NULL,
                    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
                    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
                    PRIMARY KEY (chat_jid, member_jid)
                );

                CREATE TABLE IF NOT EXISTS messages (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    source_message_id TEXT,
                    chat_jid TEXT NOT NULL,
                    sender_jid TEXT NOT NULL,
                    role TEXT NOT NULL,
                    content TEXT NOT NULL DEFAULT '',
                    is_from_me INTEGER NOT NULL DEFAULT 0,
                    media_type TEXT,
                    media_filename TEXT,
                    media_path TEXT,
                    media_base64 TEXT,
                    quoted_message_id TEXT,
                    quoted_sender TEXT,
                    quoted_content TEXT,
                    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
                );

                CREATE TABLE IF NOT EXISTS weights (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    chat_jid TEXT NOT NULL,
                    member_jid TEXT NOT NULL,
                    weight_kg REAL NOT NULL,
                    source_message_id INTEGER,
                    note TEXT NOT NULL DEFAULT '',
                    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
                );

                CREATE TABLE IF NOT EXISTS reminders (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    chat_jid TEXT NOT NULL,
                    kind TEXT NOT NULL,
                    sent_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
                    message TEXT NOT NULL
                );

                CREATE TABLE IF NOT EXISTS vision_reviews (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    message_id INTEGER NOT NULL,
                    provider TEXT NOT NULL,
                    status TEXT NOT NULL,
                    weight_kg REAL,
                    confidence REAL,
                    explanation TEXT NOT NULL DEFAULT '',
                    raw_response TEXT NOT NULL DEFAULT '',
                    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
                );
                """
            )
            self._ensure_column(conn, "messages", "media_path", "TEXT")

    def _ensure_column(self, conn: sqlite3.Connection, table: str, column: str, definition: str) -> None:
        columns = {row["name"] for row in conn.execute(f"PRAGMA table_info({table})").fetchall()}
        if column not in columns:
            conn.execute(f"ALTER TABLE {table} ADD COLUMN {column} {definition}")

    def upsert_group(self, jid: str, name: str = "") -> None:
        with self.connect() as conn:
            conn.execute(
                """
                INSERT INTO groups (jid, name) VALUES (?, ?)
                ON CONFLICT(jid) DO UPDATE SET name = excluded.name
                """,
                (jid, name),
            )

    def upsert_member(self, chat_jid: str, member_jid: str, role: str) -> None:
        with self.connect() as conn:
            conn.execute(
                """
                INSERT INTO members (chat_jid, member_jid, role) VALUES (?, ?, ?)
                ON CONFLICT(chat_jid, member_jid)
                DO UPDATE SET role = excluded.role, updated_at = CURRENT_TIMESTAMP
                """,
                (chat_jid, member_jid, role),
            )

    def store_message(self, payload: dict[str, Any], role: str) -> int:
        with self.connect() as conn:
            cursor = conn.execute(
                """
                INSERT INTO messages (
                    source_message_id, chat_jid, sender_jid, role, content, is_from_me,
                    media_type, media_filename, media_path, media_base64,
                    quoted_message_id, quoted_sender, quoted_content
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    payload.get("messageId"),
                    payload.get("chatJID", ""),
                    payload.get("sender", ""),
                    role,
                    payload.get("content", ""),
                    1 if payload.get("isFromMe") else 0,
                    payload.get("mediaType"),
                    payload.get("mediaFilename"),
                    payload.get("mediaPath"),
                    payload.get("mediaBase64"),
                    payload.get("quotedMessageId"),
                    payload.get("quotedSender"),
                    payload.get("quotedContent"),
                ),
            )
            return int(cursor.lastrowid)

    def set_message_media_path(self, message_id: int, media_path: str) -> None:
        with self.connect() as conn:
            conn.execute("UPDATE messages SET media_path = ? WHERE id = ?", (media_path, message_id))

    def store_weight(self, chat_jid: str, member_jid: str, weight_kg: float, message_id: int | None, note: str = "") -> int:
        with self.connect() as conn:
            cursor = conn.execute(
                """
                INSERT INTO weights (chat_jid, member_jid, weight_kg, source_message_id, note)
                VALUES (?, ?, ?, ?, ?)
                """,
                (chat_jid, member_jid, weight_kg, message_id, note),
            )
            return int(cursor.lastrowid)

    def store_vision_review(
        self,
        message_id: int,
        provider: str,
        status: str,
        weight_kg: float | None,
        confidence: float | None,
        explanation: str,
        raw_response: str = "",
    ) -> int:
        with self.connect() as conn:
            cursor = conn.execute(
                """
                INSERT INTO vision_reviews (message_id, provider, status, weight_kg, confidence, explanation, raw_response)
                VALUES (?, ?, ?, ?, ?, ?, ?)
                """,
                (message_id, provider, status, weight_kg, confidence, explanation, raw_response),
            )
            return int(cursor.lastrowid)

    def list_members(self, chat_jid: str) -> list[dict[str, Any]]:
        with self.connect() as conn:
            rows = conn.execute(
                "SELECT member_jid, role, created_at, updated_at FROM members WHERE chat_jid = ? ORDER BY role, member_jid",
                (chat_jid,),
            ).fetchall()
            return [dict(row) for row in rows]

    def recent_messages(self, chat_jid: str, limit: int = 20) -> list[StoredMessage]:
        with self.connect() as conn:
            rows = conn.execute(
                """
                SELECT id, chat_jid, sender_jid, role, content, media_type, created_at
                FROM messages
                WHERE chat_jid = ?
                ORDER BY id DESC
                LIMIT ?
                """,
                (chat_jid, limit),
            ).fetchall()
            return [StoredMessage(**dict(row)) for row in rows]

    def latest_reminder_date(self, chat_jid: str, kind: str) -> str | None:
        with self.connect() as conn:
            row = conn.execute(
                "SELECT date(sent_at) AS sent_date FROM reminders WHERE chat_jid = ? AND kind = ? ORDER BY id DESC LIMIT 1",
                (chat_jid, kind),
            ).fetchone()
            return str(row["sent_date"]) if row else None

    def store_reminder(self, chat_jid: str, kind: str, message: str) -> None:
        with self.connect() as conn:
            conn.execute(
                "INSERT INTO reminders (chat_jid, kind, message) VALUES (?, ?, ?)",
                (chat_jid, kind, message),
            )
