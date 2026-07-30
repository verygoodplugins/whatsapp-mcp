import json
import os
import os.path
import sqlite3
from dataclasses import dataclass
from datetime import datetime
from typing import Any

import requests

import audio

# Configuration via environment variables with sensible defaults
_DEFAULT_BRIDGE_STORE_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "whatsapp-bridge", "store")
MESSAGES_DB_PATH = os.getenv(
    "WHATSAPP_DB_PATH",
    os.path.join(_DEFAULT_BRIDGE_STORE_DIR, "messages.db"),
)
WHATSMEOW_DB_PATH = os.getenv(
    "WHATSMEOW_DB_PATH",
    os.path.join(_DEFAULT_BRIDGE_STORE_DIR, "whatsapp.db"),
)
WHATSAPP_API_BASE_URL = os.getenv("WHATSAPP_API_URL", "http://localhost:8080/api")

_BRIDGE_TOKEN_PATH = os.path.join(os.path.dirname(WHATSMEOW_DB_PATH), ".bridge-token")
_PERMISSIONS_TABLE = "mcp_chat_permissions"
_ACCESS_SETTINGS_TABLE = "mcp_access_settings"
_LEGACY_ALLOW_ALL_ENV = "WHATSAPP_MCP_LEGACY_ALLOW_ALL"


def _legacy_allow_all_enabled() -> bool:
    """Return whether the operator explicitly enabled pre-ACL behavior."""
    return os.getenv(_LEGACY_ALLOW_ALL_ENV, "").strip().lower() == "true"


def _permissions_enabled(conn: sqlite3.Connection) -> bool:
    """Return whether per-chat access control must be enforced.

    Missing or malformed ACL state fails closed. Operators temporarily running
    an older bridge may explicitly opt into its historical allow-all behavior
    with WHATSAPP_MCP_LEGACY_ALLOW_ALL=true.
    """
    row = conn.execute(
        "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?",
        (_PERMISSIONS_TABLE,),
    ).fetchone()
    return row is not None or not _legacy_allow_all_enabled()


def access_control_enabled() -> bool:
    """Best-effort access-control state for MCP wrapper behavior.

    Database errors are treated as enabled so callers fail closed instead of
    falling back to unrestricted contact metadata.
    """
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        try:
            return _permissions_enabled(conn)
        finally:
            conn.close()
    except sqlite3.Error:
        return True


def _message_read_clause(message_alias: str, access_alias: str) -> str:
    return f"""(
        ({access_alias}.read_new_since_unix IS NOT NULL
            AND unixepoch({message_alias}.timestamp) >= {access_alias}.read_new_since_unix)
        OR
        ({access_alias}.read_history_through_unix IS NOT NULL
            AND unixepoch({message_alias}.timestamp) <= {access_alias}.read_history_through_unix)
    )"""


def _chat_visible_clause(access_alias: str) -> str:
    return (
        f"({access_alias}.can_send = 1 "
        f"OR {access_alias}.read_new_since_unix IS NOT NULL "
        f"OR {access_alias}.read_history_through_unix IS NOT NULL)"
    )


def _read_bridge_token() -> str | None:
    env = os.getenv("WHATSAPP_BRIDGE_TOKEN", "").strip()
    if env:
        return env
    try:
        with open(_BRIDGE_TOKEN_PATH, encoding="utf-8") as fh:
            value = fh.read().strip()
            return value or None
    except FileNotFoundError:
        return None
    except OSError:
        return None


def _bridge_headers() -> dict[str, str]:
    token = _read_bridge_token()
    if not token:
        return {}
    return {"Authorization": f"Bearer {token}"}


@dataclass
class Message:
    timestamp: datetime
    sender: str
    content: str
    is_from_me: bool
    chat_jid: str
    id: str
    chat_name: str | None = None
    media_type: str | None = None
    # For media_type == "reaction", the bridge stores the reacted-to message ID
    # in the `filename` column. Exposed to callers as `reaction_to_message_id`.
    filename: str | None = None
    # ID of the message this one is replying to (NULL for non-replies).
    quoted_message_id: str | None = None


@dataclass
class Chat:
    jid: str
    name: str | None
    last_message_time: datetime | None
    last_message: str | None = None
    last_sender: str | None = None
    last_is_from_me: bool | None = None

    @property
    def is_group(self) -> bool:
        """Determine if chat is a group based on JID pattern."""
        return self.jid.endswith("@g.us")


@dataclass
class Contact:
    phone_number: str
    name: str | None
    jid: str


@dataclass
class MessageContext:
    message: Message
    before: list[Message]
    after: list[Message]


def msg_to_dict(message: Message, include_sender_name: bool = True) -> dict[str, Any]:
    """Convert a Message dataclass to a dictionary for JSON serialization."""
    # Extract phone number from JID (e.g., "1234567890@s.whatsapp.net" -> "1234567890")
    sender_phone = message.sender.split("@")[0] if "@" in message.sender else message.sender

    sender_name = None
    sender_display = None
    if include_sender_name:
        if message.is_from_me:
            sender_name = "Me"
            sender_display = "Me"
        else:
            resolved_name = get_sender_name(message.sender)
            # Check if we got an actual name (not just the JID back)
            if resolved_name and resolved_name != message.sender and resolved_name != sender_phone:
                sender_name = resolved_name
                sender_display = f"{resolved_name} ({sender_phone})"
            else:
                sender_name = sender_phone
                sender_display = sender_phone

    return {
        "id": message.id,
        "timestamp": message.timestamp.isoformat(),
        "sender_jid": message.sender,
        "sender_phone": sender_phone,
        "sender_name": sender_name,
        "sender_display": sender_display,  # "Name (phone)" or just phone if no name
        "content": message.content,
        "is_from_me": message.is_from_me,
        "chat_jid": message.chat_jid,
        "chat_name": message.chat_name,
        "media_type": message.media_type,
        "reaction_to_message_id": (message.filename if message.media_type == "reaction" else None),
        "quoted_message_id": message.quoted_message_id,
    }


def chat_to_dict(chat: "Chat") -> dict[str, Any]:
    """Convert a Chat dataclass to a dictionary for JSON serialization."""
    return {
        "jid": chat.jid,
        "name": chat.name,
        "is_group": chat.is_group,
        "last_message_time": chat.last_message_time.isoformat() if chat.last_message_time else None,
        "last_message": chat.last_message,
        "last_sender": chat.last_sender,
        "last_is_from_me": chat.last_is_from_me,
    }


def contact_to_dict(contact: "Contact") -> dict[str, Any]:
    """Convert a Contact dataclass to a dictionary for JSON serialization."""
    return {"phone_number": contact.phone_number, "name": contact.name, "jid": contact.jid}


def _sender_aliases(value: str) -> list[str]:
    # messages.sender is written inconsistently: the same contact may appear as
    # bare phone ("13232432100"), full phone JID ("13232432100@s.whatsapp.net"),
    # bare LID ("231241139937355"), or full LID JID ("231241139937355@lid").
    # whatsmeow_lid_map (whatsapp.db) maps pn<->lid; we emit all four forms so
    # an IN-based filter catches every row regardless of which form was stored.
    bare = value.split("@", 1)[0]
    pn: str | None = None
    lid: str | None = None
    if os.path.isfile(WHATSMEOW_DB_PATH):
        try:
            conn = sqlite3.connect(WHATSMEOW_DB_PATH)
            try:
                row = conn.execute("SELECT lid FROM whatsmeow_lid_map WHERE pn = ?", (bare,)).fetchone()
                if row:
                    pn, lid = bare, row[0]
                else:
                    row = conn.execute("SELECT pn FROM whatsmeow_lid_map WHERE lid = ?", (bare,)).fetchone()
                    if row:
                        lid, pn = bare, row[0]
            finally:
                conn.close()
        except sqlite3.Error:
            pass

    aliases: list[str] = []
    if pn:
        aliases += [pn, f"{pn}@s.whatsapp.net"]
    if lid:
        aliases += [lid, f"{lid}@lid"]
    if not aliases:
        # No mapping found; emit the bare form plus both possible suffixes so
        # we still match whichever form the bridge happened to store.
        aliases = [bare, f"{bare}@s.whatsapp.net", f"{bare}@lid"]
    return aliases


def _resolve_lid_to_phone(lid_or_jid: str) -> str | None:
    """Resolve a WhatsApp LID (linked device identifier) to a phone number.

    WhatsApp's newer protocol uses opaque LIDs (e.g. '35047067385985') as sender
    identifiers instead of phone numbers. The whatsmeow_lid_map table maps these
    back to real phone numbers.

    Returns the phone number if found, None otherwise.
    """
    if not os.path.exists(WHATSMEOW_DB_PATH):
        return None
    # Extract the numeric part from JID-style strings (e.g. '35047067385985@lid')
    lid = lid_or_jid.split("@")[0] if "@" in lid_or_jid else lid_or_jid
    try:
        conn = sqlite3.connect(WHATSMEOW_DB_PATH)
        cursor = conn.cursor()
        cursor.execute("SELECT pn FROM whatsmeow_lid_map WHERE lid = ? LIMIT 1", (lid,))
        row = cursor.fetchone()
        return row[0] if row else None
    except sqlite3.Error:
        return None
    finally:
        if "conn" in locals():
            conn.close()


def _chat_jid_candidates(value: str) -> list[str]:
    """Return exact, identity-equivalent chat JIDs in canonical order."""
    value = value.strip()
    if not value:
        return []

    if "@" in value:
        user, server = value.split("@", 1)
        # Device-qualified user JIDs are persisted as their non-device form.
        user = user.split(":", 1)[0]
        exact = f"{user}@{server}"
        if server not in ("s.whatsapp.net", "lid"):
            return [exact]
        lookup_kind = "pn" if server == "s.whatsapp.net" else "lid"
        bare = user
    else:
        exact = f"{value}@s.whatsapp.net"
        lookup_kind = "pn"
        bare = value

    pn: str | None = bare if lookup_kind == "pn" else None
    lid: str | None = bare if lookup_kind == "lid" else None
    if os.path.isfile(WHATSMEOW_DB_PATH):
        try:
            conn = sqlite3.connect(WHATSMEOW_DB_PATH)
            try:
                if lookup_kind == "pn":
                    row = conn.execute("SELECT lid FROM whatsmeow_lid_map WHERE pn = ? LIMIT 1", (bare,)).fetchone()
                    if row:
                        lid = row[0]
                    elif "@" not in value:
                        # A bare numeric identifier can also be a known LID.
                        row = conn.execute(
                            "SELECT pn FROM whatsmeow_lid_map WHERE lid = ? LIMIT 1",
                            (bare,),
                        ).fetchone()
                        if row:
                            lid, pn = bare, row[0]
                else:
                    row = conn.execute("SELECT pn FROM whatsmeow_lid_map WHERE lid = ? LIMIT 1", (bare,)).fetchone()
                    if row:
                        pn = row[0]
            finally:
                conn.close()
        except sqlite3.Error:
            # Never invent a PN/LID equivalence when the authoritative map
            # cannot be read. Exact matching remains safe.
            pass

    candidates: list[str] = []
    if pn:
        candidates.append(f"{pn}@s.whatsapp.net")
    if lid:
        candidates.append(f"{lid}@lid")
    if exact not in candidates:
        candidates.append(exact)
    return candidates


def _resolve_chat_jid(conn: sqlite3.Connection, value: str) -> str | None:
    """Resolve an input identifier to one exact JID present in chats."""
    for candidate in _chat_jid_candidates(value):
        row = conn.execute("SELECT jid FROM chats WHERE jid = ? LIMIT 1", (candidate,)).fetchone()
        if row:
            return row[0]
    return None


def _new_direct_recipient_jid(recipient: str) -> str | None:
    """Return a normalized phone JID eligible to start a new direct chat."""
    value = recipient.strip()
    if "@" in value:
        user, server = value.split("@", 1)
        if server != "s.whatsapp.net":
            return None
    else:
        user = value
    if not 7 <= len(user) <= 15 or any(character < "0" or character > "9" for character in user):
        return None
    return f"{user}@s.whatsapp.net"


def _authorize_chat_send(recipient: str, *, allow_new_conversation: bool = False) -> tuple[bool, str]:
    """Authorize an outbound operation and return its canonical recipient."""
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        try:
            if not _permissions_enabled(conn):
                return True, recipient
            chat_jid = _resolve_chat_jid(conn, recipient)
            if not chat_jid:
                new_chat_jid = _new_direct_recipient_jid(recipient)
                if allow_new_conversation and new_chat_jid:
                    row = conn.execute(
                        f"""
                        SELECT allow_start_new_conversations
                        FROM {_ACCESS_SETTINGS_TABLE}
                        WHERE singleton_id = 1
                        """
                    ).fetchone()
                    if row and row[0]:
                        return True, new_chat_jid
                return False, recipient
            row = conn.execute(
                f"SELECT can_send FROM {_PERMISSIONS_TABLE} WHERE chat_jid = ?",
                (chat_jid,),
            ).fetchone()
            return bool(row and row[0]), chat_jid
        finally:
            conn.close()
    except sqlite3.Error:
        return False, recipient


def _authorize_message_read(message_id: str, chat_jid: str) -> tuple[bool, str]:
    """Authorize access to one concrete message and return canonical chat JID."""
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        try:
            if not _permissions_enabled(conn):
                return True, chat_jid
            canonical_jid = _resolve_chat_jid(conn, chat_jid)
            if not canonical_jid:
                return False, chat_jid
            row = conn.execute(
                f"""
                SELECT 1
                FROM messages m
                JOIN {_PERMISSIONS_TABLE} access ON access.chat_jid = m.chat_jid
                WHERE m.id = ? AND m.chat_jid = ?
                    AND {_message_read_clause("m", "access")}
                LIMIT 1
                """,
                (message_id, canonical_jid),
            ).fetchone()
            return row is not None, canonical_jid
        finally:
            conn.close()
    except sqlite3.Error:
        return False, chat_jid


def _resolve_name_from_whatsmeow(jid: str) -> str | None:
    """Look up a contact name from whatsmeow's contact store (whatsapp.db).

    Handles both standard JIDs (12345@s.whatsapp.net) and LIDs (opaque numeric
    identifiers used by WhatsApp's linked device protocol). LIDs are first
    resolved to phone numbers via whatsmeow_lid_map, then looked up in contacts.

    Falls back gracefully if the DB or table doesn't exist.
    """
    if not os.path.exists(WHATSMEOW_DB_PATH):
        return None

    lookup_jid = jid
    jid_prefix = jid.split("@")[0] if "@" in jid else jid
    jid_suffix = jid.split("@")[1] if "@" in jid else ""

    # If this is a LID (@lid suffix) or a raw number, try LID map first.
    # LIDs overlap in length with phone numbers (12-15 digits) so we always
    # attempt LID resolution and fall through to direct contact lookup if not found.
    if jid_suffix in ("lid", ""):
        phone = _resolve_lid_to_phone(jid_prefix)
        if phone:
            lookup_jid = phone + "@s.whatsapp.net"
        elif jid_suffix == "lid":
            # Definitely a LID but not in the map — can't resolve
            return None

    try:
        conn = sqlite3.connect(WHATSMEOW_DB_PATH)
        cursor = conn.cursor()
        # whatsmeow_contacts columns: our_jid, their_jid, first_name, full_name, push_name, business_name
        cursor.execute(
            "SELECT full_name, push_name, first_name, business_name FROM whatsmeow_contacts WHERE their_jid = ? LIMIT 1",
            (lookup_jid,),
        )
        row = cursor.fetchone()
        if row:
            # Prefer full_name, then push_name, then first_name, then business_name
            return row[0] or row[1] or row[2] or row[3] or None
        return None
    except sqlite3.Error:
        return None
    finally:
        if "conn" in locals():
            conn.close()


def get_sender_name(sender_jid: str) -> str:
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()

        # First try matching by exact JID
        cursor.execute(
            """
            SELECT name
            FROM chats
            WHERE jid = ?
            LIMIT 1
        """,
            (sender_jid,),
        )

        result = cursor.fetchone()

        # If no result, try exact PN/LID identity aliases. Fuzzy substring
        # matching can confuse different contacts and bypass chat visibility.
        if not result:
            for candidate in _chat_jid_candidates(sender_jid):
                cursor.execute("SELECT name FROM chats WHERE jid = ? LIMIT 1", (candidate,))
                result = cursor.fetchone()
                if result:
                    break

        if result and result[0] and not result[0].replace("+", "").isdigit():
            return result[0]

        # Fall back to whatsmeow contact store
        whatsmeow_name = _resolve_name_from_whatsmeow(sender_jid)
        if whatsmeow_name:
            return whatsmeow_name

        # Try with @s.whatsapp.net suffix if bare number
        if "@" not in sender_jid:
            whatsmeow_name = _resolve_name_from_whatsmeow(sender_jid + "@s.whatsapp.net")
            if whatsmeow_name:
                return whatsmeow_name

        return sender_jid

    except sqlite3.Error as e:
        print(f"Database error while getting sender name: {e}")
        return sender_jid
    finally:
        if "conn" in locals():
            conn.close()


def format_message(message: Message, show_chat_info: bool = True) -> None:
    """Print a single message with consistent formatting."""
    output = ""

    if show_chat_info and message.chat_name:
        output += f"[{message.timestamp:%Y-%m-%d %H:%M:%S}] Chat: {message.chat_name} "
    else:
        output += f"[{message.timestamp:%Y-%m-%d %H:%M:%S}] "

    content_prefix = ""
    if hasattr(message, "media_type") and message.media_type:
        content_prefix = f"[{message.media_type} - Message ID: {message.id} - Chat JID: {message.chat_jid}] "

    try:
        sender_name = get_sender_name(message.sender) if not message.is_from_me else "Me"
        output += f"From: {sender_name}: {content_prefix}{message.content}\n"
    except Exception as e:
        print(f"Error formatting message: {e}")
    return output


def format_messages_list(messages: list[Message], show_chat_info: bool = True) -> None:
    output = ""
    if not messages:
        output += "No messages to display."
        return output

    for message in messages:
        output += format_message(message, show_chat_info)
    return output


def list_messages(
    after: str | None = None,
    before: str | None = None,
    sender_phone_number: str | None = None,
    chat_jid: str | None = None,
    query: str | None = None,
    limit: int = 20,
    page: int = 0,
    include_context: bool = True,
    context_before: int = 1,
    context_after: int = 1,
    sort_by: str = "newest",
) -> list[dict[str, Any]]:
    """Get messages matching the specified criteria with optional context.

    Args:
        after: Optional ISO-8601 formatted string to only return messages after this date
        before: Optional ISO-8601 formatted string to only return messages before this date
        sender_phone_number: Optional phone number to filter messages by sender
        chat_jid: Optional chat JID to filter messages by chat
        query: Optional search term to filter messages by content
        limit: Maximum number of messages to return (default 20)
        page: Page number for pagination (default 0)
        include_context: Whether to include messages before and after matches (default True)
        context_before: Number of messages to include before each match (default 1)
        context_after: Number of messages to include after each match (default 1)
        sort_by: Sort order - "newest" (default) or "oldest" for chronological ordering

    Returns:
        List of message dictionaries with id, timestamp, sender, content, etc.
    """
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()
        permissions_enabled = _permissions_enabled(conn)

        # Build base query
        query_parts = [
            "SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type, messages.quoted_message_id, messages.filename FROM messages"
        ]
        query_parts.append("JOIN chats ON messages.chat_jid = chats.jid")
        if permissions_enabled:
            query_parts.append(f"JOIN {_PERMISSIONS_TABLE} access ON access.chat_jid = messages.chat_jid")
        where_clauses = [_message_read_clause("messages", "access")] if permissions_enabled else []
        params = []

        # Add filters
        if after:
            try:
                after = datetime.fromisoformat(after)
            except ValueError:
                raise ValueError(f"Invalid date format for 'after': {after}. Please use ISO-8601 format.")

            where_clauses.append("messages.timestamp > ?")
            params.append(after)

        if before:
            try:
                before = datetime.fromisoformat(before)
            except ValueError:
                raise ValueError(f"Invalid date format for 'before': {before}. Please use ISO-8601 format.")

            where_clauses.append("messages.timestamp < ?")
            params.append(before)

        if sender_phone_number:
            aliases = _sender_aliases(sender_phone_number)
            placeholders = ",".join("?" * len(aliases))
            where_clauses.append(f"messages.sender IN ({placeholders})")
            params.extend(aliases)

        if chat_jid:
            if permissions_enabled:
                chat_jid = _resolve_chat_jid(conn, chat_jid)
                if not chat_jid:
                    return []
            where_clauses.append("messages.chat_jid = ?")
            params.append(chat_jid)

        if query:
            # SQLite's LOWER() only handles ASCII, so LIKE LOWER(...) silently
            # excludes Unicode matches. instr() on the raw column preserves them.
            where_clauses.append("(instr(LOWER(messages.content), LOWER(?)) > 0 OR instr(messages.content, ?) > 0)")
            params.extend([query, query])

        if where_clauses:
            query_parts.append("WHERE " + " AND ".join(where_clauses))

        # Add sorting and pagination
        offset = page * limit
        order = "DESC" if sort_by == "newest" else "ASC"
        query_parts.append(f"ORDER BY messages.timestamp {order}")
        query_parts.append("LIMIT ? OFFSET ?")
        params.extend([limit, offset])

        cursor.execute(" ".join(query_parts), tuple(params))
        messages = cursor.fetchall()

        result = []
        for msg in messages:
            message = Message(
                timestamp=datetime.fromisoformat(msg[0]),
                sender=msg[1],
                chat_name=msg[2],
                content=msg[3],
                is_from_me=msg[4],
                chat_jid=msg[5],
                id=msg[6],
                media_type=msg[7],
                quoted_message_id=msg[8] if len(msg) > 8 else None,
                filename=msg[9] if len(msg) > 9 else None,
            )
            result.append(message)

        if include_context and result:
            # Add context for each message, deduplicated by message ID
            seen_ids = set()
            messages_with_context = []
            for msg in result:
                context = get_message_context(
                    msg.id,
                    context_before,
                    context_after,
                    chat_jid=msg.chat_jid,
                )
                for ctx_msg in context.before:
                    message_key = (ctx_msg.chat_jid, ctx_msg.id)
                    if message_key not in seen_ids:
                        seen_ids.add(message_key)
                        messages_with_context.append(ctx_msg)
                message_key = (context.message.chat_jid, context.message.id)
                if message_key not in seen_ids:
                    seen_ids.add(message_key)
                    messages_with_context.append(context.message)
                for ctx_msg in context.after:
                    message_key = (ctx_msg.chat_jid, ctx_msg.id)
                    if message_key not in seen_ids:
                        seen_ids.add(message_key)
                        messages_with_context.append(ctx_msg)

            return [msg_to_dict(msg) for msg in messages_with_context]

        # Return messages without context
        return [msg_to_dict(msg) for msg in result]

    except sqlite3.Error as e:
        print(f"Database error: {e}")
        return []
    finally:
        if "conn" in locals():
            conn.close()


def get_message_context(
    message_id: str,
    before: int = 5,
    after: int = 5,
    chat_jid: str | None = None,
) -> MessageContext:
    """Get context around a specific message."""
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()
        permissions_enabled = _permissions_enabled(conn)

        # Get the target message first
        target_query = """
            SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.chat_jid, messages.media_type, messages.quoted_message_id, messages.filename
            FROM messages
            JOIN chats ON messages.chat_jid = chats.jid
        """
        if permissions_enabled:
            target_query += f" JOIN {_PERMISSIONS_TABLE} access ON access.chat_jid = messages.chat_jid"

        target_where = ["messages.id = ?"]
        target_params: list[Any] = [message_id]
        if chat_jid:
            if permissions_enabled:
                chat_jid = _resolve_chat_jid(conn, chat_jid)
                if not chat_jid:
                    raise ValueError("Message not found or not readable")
            target_where.append("messages.chat_jid = ?")
            target_params.append(chat_jid)
        if permissions_enabled:
            target_where.append(_message_read_clause("messages", "access"))

        target_query += " WHERE " + " AND ".join(target_where)
        target_query += " LIMIT 2"
        cursor.execute(target_query, tuple(target_params))
        target_rows = cursor.fetchall()

        if not target_rows:
            raise ValueError("Message not found or not readable")
        if len(target_rows) > 1:
            raise ValueError("Message ID is ambiguous; provide chat_jid")
        msg_data = target_rows[0]

        target_message = Message(
            timestamp=datetime.fromisoformat(msg_data[0]),
            sender=msg_data[1],
            chat_name=msg_data[2],
            content=msg_data[3],
            is_from_me=msg_data[4],
            chat_jid=msg_data[5],
            id=msg_data[6],
            media_type=msg_data[8],
            quoted_message_id=msg_data[9] if len(msg_data) > 9 else None,
            filename=msg_data[10] if len(msg_data) > 10 else None,
        )

        # Get messages before
        before_query = """
            SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type, messages.quoted_message_id, messages.filename
            FROM messages
            JOIN chats ON messages.chat_jid = chats.jid
        """
        if permissions_enabled:
            before_query += f" JOIN {_PERMISSIONS_TABLE} access ON access.chat_jid = messages.chat_jid"
        before_query += """
            WHERE messages.chat_jid = ? AND messages.timestamp < ?
        """
        if permissions_enabled:
            before_query += f" AND {_message_read_clause('messages', 'access')}"
        before_query += """
            ORDER BY messages.timestamp DESC
            LIMIT ?
        """
        cursor.execute(
            before_query,
            (msg_data[7], msg_data[0], before),
        )

        before_messages = []
        for msg in cursor.fetchall():
            before_messages.append(
                Message(
                    timestamp=datetime.fromisoformat(msg[0]),
                    sender=msg[1],
                    chat_name=msg[2],
                    content=msg[3],
                    is_from_me=msg[4],
                    chat_jid=msg[5],
                    id=msg[6],
                    media_type=msg[7],
                    quoted_message_id=msg[8] if len(msg) > 8 else None,
                    filename=msg[9] if len(msg) > 9 else None,
                )
            )

        # Get messages after
        after_query = """
            SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type, messages.quoted_message_id, messages.filename
            FROM messages
            JOIN chats ON messages.chat_jid = chats.jid
        """
        if permissions_enabled:
            after_query += f" JOIN {_PERMISSIONS_TABLE} access ON access.chat_jid = messages.chat_jid"
        after_query += """
            WHERE messages.chat_jid = ? AND messages.timestamp > ?
        """
        if permissions_enabled:
            after_query += f" AND {_message_read_clause('messages', 'access')}"
        after_query += """
            ORDER BY messages.timestamp ASC
            LIMIT ?
        """
        cursor.execute(
            after_query,
            (msg_data[7], msg_data[0], after),
        )

        after_messages = []
        for msg in cursor.fetchall():
            after_messages.append(
                Message(
                    timestamp=datetime.fromisoformat(msg[0]),
                    sender=msg[1],
                    chat_name=msg[2],
                    content=msg[3],
                    is_from_me=msg[4],
                    chat_jid=msg[5],
                    id=msg[6],
                    media_type=msg[7],
                    quoted_message_id=msg[8] if len(msg) > 8 else None,
                    filename=msg[9] if len(msg) > 9 else None,
                )
            )

        return MessageContext(message=target_message, before=before_messages, after=after_messages)

    except sqlite3.Error as e:
        print(f"Database error: {e}")
        raise
    finally:
        if "conn" in locals():
            conn.close()


def list_chats(
    query: str | None = None,
    limit: int = 20,
    page: int = 0,
    include_last_message: bool = True,
    sort_by: str = "last_active",
) -> list[dict[str, Any]]:
    """Get chats matching the specified criteria.

    Returns:
        List of chat dictionaries with jid, name, is_group, last_message, etc.
    """
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()
        permissions_enabled = _permissions_enabled(conn)

        # Build base query. The last-message columns are referenced by tuple
        # index downstream, so we keep the result shape constant and emit
        # static NULLs when the messages table is not joined — otherwise the
        # SELECT references messages.* with no FROM/JOIN and SQLite errors
        # out with "no such column: messages.content".
        if permissions_enabled:
            if include_last_message:
                last_message_select = (
                    "allowed_last.content as last_message, "
                    "allowed_last.sender as last_sender, "
                    "allowed_last.is_from_me as last_is_from_me"
                )
            else:
                last_message_select = "NULL as last_message, NULL as last_sender, NULL as last_is_from_me"
            query_parts = [
                f"""
                SELECT
                    chats.jid,
                    chats.name,
                    allowed_last.timestamp,
                    {last_message_select}
                FROM chats
                JOIN {_PERMISSIONS_TABLE} access ON access.chat_jid = chats.jid
                LEFT JOIN messages allowed_last ON allowed_last.rowid = (
                    SELECT candidate.rowid
                    FROM messages candidate
                    WHERE candidate.chat_jid = chats.jid
                        AND {_message_read_clause("candidate", "access")}
                    ORDER BY candidate.timestamp DESC, candidate.rowid DESC
                    LIMIT 1
                )
                """
            ]
            where_clauses = [_chat_visible_clause("access")]
        else:
            if include_last_message:
                last_message_select = (
                    "messages.content as last_message, "
                    "messages.sender as last_sender, "
                    "messages.is_from_me as last_is_from_me"
                )
            else:
                last_message_select = "NULL as last_message, NULL as last_sender, NULL as last_is_from_me"

            query_parts = [
                f"""
                SELECT
                    chats.jid,
                    chats.name,
                    chats.last_message_time,
                    {last_message_select}
                FROM chats
                """
            ]

            if include_last_message:
                query_parts.append("""
                    LEFT JOIN messages ON chats.jid = messages.chat_jid
                    AND chats.last_message_time = messages.timestamp
                """)
            where_clauses = []
        params = []

        if query:
            # instr() on the raw column matches Unicode; LOWER()+LIKE only covers ASCII.
            where_clauses.append(
                "(instr(LOWER(chats.name), LOWER(?)) > 0 OR instr(chats.name, ?) > 0 OR chats.jid LIKE ?)"
            )
            params.extend([query, query, f"%{query}%"])

        if where_clauses:
            query_parts.append("WHERE " + " AND ".join(where_clauses))

        # Add sorting
        if sort_by == "last_active":
            order_by = (
                "allowed_last.timestamp DESC, chats.name" if permissions_enabled else "chats.last_message_time DESC"
            )
        else:
            order_by = "chats.name"
        query_parts.append(f"ORDER BY {order_by}")

        # Add pagination
        offset = (page) * limit
        query_parts.append("LIMIT ? OFFSET ?")
        params.extend([limit, offset])

        cursor.execute(" ".join(query_parts), tuple(params))
        chats = cursor.fetchall()

        result = []
        for chat_data in chats:
            chat = Chat(
                jid=chat_data[0],
                name=chat_data[1],
                last_message_time=datetime.fromisoformat(chat_data[2]) if chat_data[2] else None,
                last_message=chat_data[3],
                last_sender=chat_data[4],
                last_is_from_me=chat_data[5],
            )
            result.append(chat_to_dict(chat))

        return result

    except sqlite3.Error as e:
        print(f"Database error: {e}")
        return []
    finally:
        if "conn" in locals():
            conn.close()


def search_contacts(query: str) -> list[dict[str, Any]]:
    """Search contacts by name or phone number.

    Searches both the messages.db chats table and whatsmeow's contact store
    (whatsapp.db) to find contacts. Results are deduplicated by JID.
    """
    seen_jids: set[str] = set()
    result: list[dict[str, Any]] = []
    # JIDs are all ASCII so LIKE is safe; names use instr() because SQLite's
    # LOWER() only folds case for ASCII and would drop Unicode matches.
    jid_pattern = "%" + query + "%"
    permissions_enabled = False
    allowed_contact_jids: set[str] = set()

    # 1) Search messages.db chats table (existing behavior)
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()
        permissions_enabled = _permissions_enabled(conn)
        messages_query = """
            SELECT DISTINCT jid, name
            FROM chats
        """
        params: list[Any] = []
        if permissions_enabled:
            messages_query += f" JOIN {_PERMISSIONS_TABLE} access ON access.chat_jid = chats.jid"
        messages_query += """
            WHERE
                (instr(LOWER(name), LOWER(?)) > 0 OR instr(name, ?) > 0 OR jid LIKE ?)
                AND jid NOT LIKE '%@g.us'
        """
        params.extend([query, query, jid_pattern])
        if permissions_enabled:
            messages_query += f" AND {_chat_visible_clause('access')}"
            allowed_rows = conn.execute(
                f"""
                SELECT chats.jid
                FROM chats
                JOIN {_PERMISSIONS_TABLE} access ON access.chat_jid = chats.jid
                WHERE chats.jid NOT LIKE '%@g.us'
                    AND {_chat_visible_clause("access")}
                """
            ).fetchall()
            for (allowed_jid,) in allowed_rows:
                allowed_contact_jids.update(_chat_jid_candidates(allowed_jid))
        messages_query += " ORDER BY name, jid LIMIT 50"
        cursor.execute(messages_query, tuple(params))
        for jid, name in cursor.fetchall():
            if jid not in seen_jids:
                seen_jids.add(jid)
                contact = Contact(phone_number=jid.split("@")[0], name=name, jid=jid)
                result.append(contact_to_dict(contact))
    except sqlite3.Error as e:
        print(f"Database error (messages.db): {e}")
        return []
    finally:
        if "conn" in locals():
            conn.close()

    # 2) Search whatsmeow contact store (whatsapp.db)
    if os.path.exists(WHATSMEOW_DB_PATH):
        try:
            conn2 = sqlite3.connect(WHATSMEOW_DB_PATH)
            cursor2 = conn2.cursor()
            whatsmeow_query = """
                SELECT their_jid, full_name, push_name, first_name, business_name
                FROM whatsmeow_contacts
                WHERE
                    instr(LOWER(full_name), LOWER(?)) > 0 OR instr(full_name, ?) > 0
                    OR instr(LOWER(push_name), LOWER(?)) > 0 OR instr(push_name, ?) > 0
                    OR instr(LOWER(first_name), LOWER(?)) > 0 OR instr(first_name, ?) > 0
                    OR instr(LOWER(business_name), LOWER(?)) > 0 OR instr(business_name, ?) > 0
                    OR their_jid LIKE ?
            """
            if not permissions_enabled:
                whatsmeow_query += " LIMIT 50"
            cursor2.execute(
                whatsmeow_query,
                (query, query, query, query, query, query, query, query, jid_pattern),
            )
            for their_jid, full_name, push_name, first_name, business_name in cursor2.fetchall():
                if permissions_enabled and not (set(_chat_jid_candidates(their_jid)) & allowed_contact_jids):
                    continue
                if their_jid not in seen_jids:
                    seen_jids.add(their_jid)
                    name = full_name or push_name or first_name or business_name or ""
                    contact = Contact(phone_number=their_jid.split("@")[0], name=name, jid=their_jid)
                    result.append(contact_to_dict(contact))
                    if permissions_enabled and len(result) >= 50:
                        break
        except sqlite3.Error as e:
            print(f"Database error (whatsapp.db): {e}")
        finally:
            if "conn2" in locals():
                conn2.close()

    return result


def get_contact_chats(jid: str, limit: int = 20, page: int = 0) -> list[dict[str, Any]]:
    """Get all chats involving the contact.

    Args:
        jid: The contact's JID to search for
        limit: Maximum number of chats to return (default 20)
        page: Page number for pagination (default 0)
    """
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()

        aliases = _sender_aliases(jid)
        placeholders = ",".join("?" * len(aliases))
        if _permissions_enabled(conn):
            canonical_jid = _resolve_chat_jid(conn, jid)
            cursor.execute(
                f"""
                SELECT DISTINCT
                    c.jid,
                    c.name,
                    allowed_last.timestamp,
                    allowed_last.content as last_message,
                    allowed_last.sender as last_sender,
                    allowed_last.is_from_me as last_is_from_me
                FROM chats c
                JOIN {_PERMISSIONS_TABLE} access ON access.chat_jid = c.jid
                LEFT JOIN messages allowed_last ON allowed_last.rowid = (
                    SELECT candidate.rowid
                    FROM messages candidate
                    WHERE candidate.chat_jid = c.jid
                        AND {_message_read_clause("candidate", "access")}
                    ORDER BY candidate.timestamp DESC, candidate.rowid DESC
                    LIMIT 1
                )
                WHERE {_chat_visible_clause("access")}
                    AND (
                        EXISTS (
                            SELECT 1
                            FROM messages contact_msg
                            WHERE contact_msg.chat_jid = c.jid
                                AND contact_msg.sender IN ({placeholders})
                                AND {_message_read_clause("contact_msg", "access")}
                        )
                        OR c.jid = ?
                    )
                ORDER BY allowed_last.timestamp DESC
                LIMIT ? OFFSET ?
                """,
                (*aliases, canonical_jid or "", limit, page * limit),
            )
        else:
            cursor.execute(
                f"""
                SELECT DISTINCT
                    c.jid,
                    c.name,
                    c.last_message_time,
                    last_msg.content as last_message,
                    last_msg.sender as last_sender,
                    last_msg.is_from_me as last_is_from_me
                FROM chats c
                LEFT JOIN messages last_msg ON c.jid = last_msg.chat_jid
                    AND c.last_message_time = last_msg.timestamp
                WHERE EXISTS (
                    SELECT 1
                    FROM messages contact_msg
                    WHERE contact_msg.chat_jid = c.jid
                        AND contact_msg.sender IN ({placeholders})
                ) OR c.jid = ?
                ORDER BY c.last_message_time DESC
                LIMIT ? OFFSET ?
                """,
                (*aliases, jid, limit, page * limit),
            )

        chats = cursor.fetchall()

        result = []
        for chat_data in chats:
            chat = Chat(
                jid=chat_data[0],
                name=chat_data[1],
                last_message_time=datetime.fromisoformat(chat_data[2]) if chat_data[2] else None,
                last_message=chat_data[3],
                last_sender=chat_data[4],
                last_is_from_me=chat_data[5],
            )
            result.append(chat_to_dict(chat))

        return result

    except sqlite3.Error as e:
        print(f"Database error: {e}")
        return []
    finally:
        if "conn" in locals():
            conn.close()


def get_last_interaction(jid: str) -> dict[str, Any] | None:
    """Get most recent message involving the contact.

    Args:
        jid: The JID of the contact to search for

    Returns:
        Message dictionary or None if no messages found
    """
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()

        aliases = _sender_aliases(jid)
        placeholders = ",".join("?" * len(aliases))
        permissions_enabled = _permissions_enabled(conn)
        canonical_jid = _resolve_chat_jid(conn, jid) if permissions_enabled else jid
        interaction_query = """
            SELECT
                m.timestamp,
                m.sender,
                c.name,
                m.content,
                m.is_from_me,
                c.jid,
                m.id,
                m.media_type
            FROM messages m
            JOIN chats c ON m.chat_jid = c.jid
        """
        if permissions_enabled:
            interaction_query += f" JOIN {_PERMISSIONS_TABLE} access ON access.chat_jid = m.chat_jid"
        interaction_query += f" WHERE (m.sender IN ({placeholders}) OR c.jid = ?)"
        if permissions_enabled:
            interaction_query += f" AND {_message_read_clause('m', 'access')}"
        interaction_query += """
            ORDER BY m.timestamp DESC
            LIMIT 1
        """
        cursor.execute(
            interaction_query,
            (*aliases, canonical_jid or ""),
        )

        msg_data = cursor.fetchone()

        if not msg_data:
            return None

        message = Message(
            timestamp=datetime.fromisoformat(msg_data[0]),
            sender=msg_data[1],
            chat_name=msg_data[2],
            content=msg_data[3],
            is_from_me=msg_data[4],
            chat_jid=msg_data[5],
            id=msg_data[6],
            media_type=msg_data[7],
        )

        return msg_to_dict(message)

    except sqlite3.Error as e:
        print(f"Database error: {e}")
        return None
    finally:
        if "conn" in locals():
            conn.close()


def get_chat(chat_jid: str, include_last_message: bool = True) -> dict[str, Any] | None:
    """Get chat metadata by JID.

    Returns:
        Chat dictionary or None if not found
    """
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()
        permissions_enabled = _permissions_enabled(conn)

        # See list_chats: keep result tuple shape stable across the
        # include_last_message branch by emitting static NULLs when we
        # don't JOIN the messages table.
        if permissions_enabled:
            chat_jid = _resolve_chat_jid(conn, chat_jid)
            if not chat_jid:
                return None
            if include_last_message:
                last_message_select = (
                    "m.content as last_message, m.sender as last_sender, m.is_from_me as last_is_from_me"
                )
            else:
                last_message_select = "NULL as last_message, NULL as last_sender, NULL as last_is_from_me"
            query = f"""
                SELECT
                    c.jid,
                    c.name,
                    m.timestamp,
                    {last_message_select}
                FROM chats c
                JOIN {_PERMISSIONS_TABLE} access ON access.chat_jid = c.jid
                LEFT JOIN messages m ON m.rowid = (
                    SELECT candidate.rowid
                    FROM messages candidate
                    WHERE candidate.chat_jid = c.jid
                        AND {_message_read_clause("candidate", "access")}
                    ORDER BY candidate.timestamp DESC, candidate.rowid DESC
                    LIMIT 1
                )
                WHERE c.jid = ? AND {_chat_visible_clause("access")}
            """
        else:
            if include_last_message:
                last_message_select = (
                    "m.content as last_message, m.sender as last_sender, m.is_from_me as last_is_from_me"
                )
            else:
                last_message_select = "NULL as last_message, NULL as last_sender, NULL as last_is_from_me"

            query = f"""
                SELECT
                    c.jid,
                    c.name,
                    c.last_message_time,
                    {last_message_select}
                FROM chats c
            """

            if include_last_message:
                query += """
                    LEFT JOIN messages m ON c.jid = m.chat_jid
                    AND c.last_message_time = m.timestamp
                """

            query += " WHERE c.jid = ?"

        cursor.execute(query, (chat_jid,))
        chat_data = cursor.fetchone()

        if not chat_data:
            return None

        chat = Chat(
            jid=chat_data[0],
            name=chat_data[1],
            last_message_time=datetime.fromisoformat(chat_data[2]) if chat_data[2] else None,
            last_message=chat_data[3],
            last_sender=chat_data[4],
            last_is_from_me=chat_data[5],
        )
        return chat_to_dict(chat)

    except sqlite3.Error as e:
        print(f"Database error: {e}")
        return None
    finally:
        if "conn" in locals():
            conn.close()


def get_direct_chat_by_contact(sender_phone_number: str) -> dict[str, Any] | None:
    """Get chat metadata by sender phone number."""
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()

        if _permissions_enabled(conn):
            chat_jid = _resolve_chat_jid(conn, sender_phone_number)
            if not chat_jid or chat_jid.endswith("@g.us"):
                return None
            return get_chat(chat_jid)

        cursor.execute(
            """
            SELECT
                c.jid,
                c.name,
                c.last_message_time,
                m.content as last_message,
                m.sender as last_sender,
                m.is_from_me as last_is_from_me
            FROM chats c
            LEFT JOIN messages m ON c.jid = m.chat_jid
                AND c.last_message_time = m.timestamp
            WHERE c.jid LIKE ? AND c.jid NOT LIKE '%@g.us'
            LIMIT 1
        """,
            (f"%{sender_phone_number}%",),
        )

        chat_data = cursor.fetchone()

        if not chat_data:
            return None

        chat = Chat(
            jid=chat_data[0],
            name=chat_data[1],
            last_message_time=datetime.fromisoformat(chat_data[2]) if chat_data[2] else None,
            last_message=chat_data[3],
            last_sender=chat_data[4],
            last_is_from_me=chat_data[5],
        )
        return chat_to_dict(chat)

    except sqlite3.Error as e:
        print(f"Database error: {e}")
        return None
    finally:
        if "conn" in locals():
            conn.close()


def send_message(
    recipient: str,
    message: str,
    quoted_message_id: str = "",
    quoted_sender_jid: str = "",
    quoted_content: str = "",
) -> tuple[bool, str]:
    try:
        # Validate input
        if not recipient:
            return False, "Recipient must be provided"
        allowed, canonical_recipient = _authorize_chat_send(
            recipient,
            allow_new_conversation=not quoted_message_id,
        )
        if not allowed:
            return False, "Sending is not allowed for this chat"
        recipient = canonical_recipient

        url = f"{WHATSAPP_API_BASE_URL}/send"
        payload: dict[str, Any] = {
            "recipient": recipient,
            "message": message,
        }
        if quoted_message_id:
            payload["quoted_message_id"] = quoted_message_id
            payload["quoted_sender_jid"] = quoted_sender_jid
            payload["quoted_content"] = quoted_content

        response = requests.post(url, json=payload, headers=_bridge_headers())

        # Check if the request was successful
        if response.status_code == 200:
            result = response.json()
            return result.get("success", False), result.get("message", "Unknown response")
        else:
            return False, f"Error: HTTP {response.status_code} - {response.text}"

    except requests.RequestException as e:
        return False, f"Request error: {str(e)}"
    except json.JSONDecodeError:
        return False, f"Error parsing response: {response.text}"
    except Exception as e:
        return False, f"Unexpected error: {str(e)}"


def send_file(recipient: str, media_path: str) -> tuple[bool, str]:
    try:
        # Validate input
        if not recipient:
            return False, "Recipient must be provided"
        allowed, canonical_recipient = _authorize_chat_send(
            recipient,
            allow_new_conversation=True,
        )
        if not allowed:
            return False, "Sending is not allowed for this chat"
        recipient = canonical_recipient

        if not media_path:
            return False, "Media path must be provided"

        if not os.path.isfile(media_path):
            return False, f"Media file not found: {media_path}"

        url = f"{WHATSAPP_API_BASE_URL}/send"
        payload = {"recipient": recipient, "media_path": media_path}

        response = requests.post(url, json=payload, headers=_bridge_headers())

        # Check if the request was successful
        if response.status_code == 200:
            result = response.json()
            return result.get("success", False), result.get("message", "Unknown response")
        else:
            return False, f"Error: HTTP {response.status_code} - {response.text}"

    except requests.RequestException as e:
        return False, f"Request error: {str(e)}"
    except json.JSONDecodeError:
        return False, f"Error parsing response: {response.text}"
    except Exception as e:
        return False, f"Unexpected error: {str(e)}"


def send_audio_message(recipient: str, media_path: str) -> tuple[bool, str]:
    try:
        # Validate input
        if not recipient:
            return False, "Recipient must be provided"
        allowed, canonical_recipient = _authorize_chat_send(
            recipient,
            allow_new_conversation=True,
        )
        if not allowed:
            return False, "Sending is not allowed for this chat"
        recipient = canonical_recipient

        if not media_path:
            return False, "Media path must be provided"

        if not os.path.isfile(media_path):
            return False, f"Media file not found: {media_path}"

        if not media_path.endswith(".ogg"):
            try:
                media_path = audio.convert_to_opus_ogg_temp(media_path)
            except Exception as e:
                return False, f"Error converting file to opus ogg. You likely need to install ffmpeg: {str(e)}"

        url = f"{WHATSAPP_API_BASE_URL}/send"
        payload = {"recipient": recipient, "media_path": media_path}

        response = requests.post(url, json=payload, headers=_bridge_headers())

        # Check if the request was successful
        if response.status_code == 200:
            result = response.json()
            return result.get("success", False), result.get("message", "Unknown response")
        else:
            return False, f"Error: HTTP {response.status_code} - {response.text}"

    except requests.RequestException as e:
        return False, f"Request error: {str(e)}"
    except json.JSONDecodeError:
        return False, f"Error parsing response: {response.text}"
    except Exception as e:
        return False, f"Unexpected error: {str(e)}"


def send_reaction(
    recipient: str,
    message_id: str,
    emoji: str,
    from_me: bool = False,
    sender_jid: str = "",
) -> tuple[bool, str]:
    """Send (or remove) a reaction to a WhatsApp message.

    Args:
        recipient: The chat JID the message belongs to (phone JID or group JID).
        message_id: The ID of the message to react to.
        emoji: The reaction emoji. Pass an empty string to remove an existing reaction.
        from_me: Whether the original message was sent by the current user.
        sender_jid: JID of the original message sender (required for group messages
                    when from_me is False so the bridge can build the correct key).

    Returns:
        Tuple of (success, status_message).
    """
    try:
        if not recipient:
            return False, "Recipient must be provided"
        if not message_id:
            return False, "Message ID must be provided"
        allowed, canonical_recipient = _authorize_chat_send(recipient)
        if not allowed:
            return False, "Sending is not allowed for this chat"
        recipient = canonical_recipient

        url = f"{WHATSAPP_API_BASE_URL}/react"
        payload: dict[str, Any] = {
            "recipient": recipient,
            "message_id": message_id,
            "emoji": emoji,
            "from_me": from_me,
            "sender_jid": sender_jid,
        }

        response = requests.post(url, json=payload, headers=_bridge_headers())

        if response.status_code == 200:
            result = response.json()
            if result.get("ok"):
                return True, "Reaction sent"
            return False, result.get("error", "Unknown error")
        else:
            return False, f"Error: HTTP {response.status_code} - {response.text}"

    except requests.RequestException as e:
        return False, f"Request error: {str(e)}"
    except json.JSONDecodeError:
        return False, f"Error parsing response: {response.text}"
    except Exception as e:
        return False, f"Unexpected error: {str(e)}"


def download_media(message_id: str, chat_jid: str) -> str | None:
    """Download media from a message and return the local file path.

    Args:
        message_id: The ID of the message containing the media
        chat_jid: The JID of the chat containing the message

    Returns:
        The local file path if download was successful, None otherwise
    """
    try:
        allowed, canonical_chat_jid = _authorize_message_read(message_id, chat_jid)
        if not allowed:
            print("Download denied by chat access policy")
            return None
        chat_jid = canonical_chat_jid

        url = f"{WHATSAPP_API_BASE_URL}/download"
        payload = {"message_id": message_id, "chat_jid": chat_jid}

        response = requests.post(url, json=payload, headers=_bridge_headers())

        if response.status_code == 200:
            result = response.json()
            if result.get("success", False):
                path = result.get("path")
                print(f"Media downloaded successfully: {path}")
                return path
            else:
                print(f"Download failed: {result.get('message', 'Unknown error')}")
                return None
        else:
            print(f"Error: HTTP {response.status_code} - {response.text}")
            return None

    except requests.RequestException as e:
        print(f"Request error: {str(e)}")
        return None
    except json.JSONDecodeError:
        print(f"Error parsing response: {response.text}")
        return None
    except Exception as e:
        print(f"Unexpected error: {str(e)}")
        return None
