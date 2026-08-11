"""Tests for full-text search in list_messages.

The previous implementation matched with instr(), which is a substring scan:
"ana" matched inside "semana", "orcamento" never matched "orçamento", and a
two-word query only matched when both words happened to be adjacent. These
tests pin the FTS5 behaviour that replaces it, and the fallback that keeps the
old behaviour working when the index cannot be built.
"""

import sqlite3

import pytest

import whatsapp

SCHEMA = """
CREATE TABLE chats (
    jid TEXT PRIMARY KEY,
    name TEXT,
    last_message_time TIMESTAMP
);
CREATE TABLE messages (
    id TEXT,
    chat_jid TEXT,
    sender TEXT,
    content TEXT,
    timestamp TIMESTAMP,
    is_from_me BOOLEAN,
    media_type TEXT,
    filename TEXT,
    url TEXT,
    media_key BLOB,
    file_sha256 BLOB,
    file_enc_sha256 BLOB,
    file_length INTEGER,
    deleted_at TIMESTAMP,
    quoted_message_id TEXT,
    PRIMARY KEY (id, chat_jid),
    FOREIGN KEY (chat_jid) REFERENCES chats(jid)
);
"""

CHAT = "1234567890@s.whatsapp.net"

# (id, content). Timestamps are assigned in order so "newest" is deterministic.
ROWS = [
    ("m1", "Preciso do orçamento da obra"),
    ("m2", "Na semana passada foi analisado"),
    ("m3", "A Ana chegou"),
    ("m4", "o boleto venceu"),
    ("m5", "confirmo o pagamento hoje"),
    ("m6", "orçamentos revisados"),
    ("m7", "会議の資料を送ります"),
    ("m8", "مرحبا بالعالم"),
    ("m9", ""),
]


def _make_db(path):
    conn = sqlite3.connect(path)
    conn.executescript(SCHEMA)
    conn.execute(
        "INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
        (CHAT, "Alice", "2024-01-15 10:00:00+00:00"),
    )
    for i, (mid, content) in enumerate(ROWS):
        conn.execute(
            "INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me) VALUES (?, ?, ?, ?, ?, ?)",
            (mid, CHAT, "1234567890", content, f"2024-01-15 10:{i:02d}:00+00:00", 0),
        )
    conn.commit()
    conn.close()


@pytest.fixture
def messages_db(tmp_path, monkeypatch):
    db_path = tmp_path / "messages.db"
    _make_db(str(db_path))
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(db_path))
    # The readiness cache is keyed by path and lives for the process, so clear it
    # to keep tests independent of each other's temp databases.
    whatsapp._fts_ready.clear()
    return db_path


def _contents(**kwargs):
    kwargs.setdefault("include_context", False)
    kwargs.setdefault("limit", 50)
    return [m["content"] for m in whatsapp.list_messages(**kwargs)]


def test_index_is_created_on_first_search(messages_db):
    """The caller does not have to migrate anything by hand."""
    conn = sqlite3.connect(str(messages_db))
    assert conn.execute("SELECT 1 FROM sqlite_master WHERE name = ?", (whatsapp.FTS_TABLE,)).fetchone() is None
    conn.close()

    _contents(query="obra")

    conn = sqlite3.connect(str(messages_db))
    assert conn.execute("SELECT 1 FROM sqlite_master WHERE name = ?", (whatsapp.FTS_TABLE,)).fetchone() is not None
    conn.close()


def test_diacritics_are_folded(messages_db):
    """Typing without the accent finds the accented word, and the reverse."""
    assert "Preciso do orçamento da obra" in _contents(query="orcamento")
    assert "Preciso do orçamento da obra" in _contents(query="orçamento")


def test_matches_whole_words_not_substrings(messages_db):
    """ "ana" must not match "semana" or "analisado"."""
    found = _contents(query="ana")
    assert found == ["A Ana chegou"]


def test_terms_do_not_have_to_be_adjacent(messages_db):
    """Two words match a message containing both, in any position."""
    assert _contents(query="boleto OR pagamento") != []
    assert _contents(query="orcamento AND obra") == ["Preciso do orçamento da obra"]


def test_prefix_search(messages_db):
    """A trailing * matches the singular and the plural."""
    found = _contents(query="orcament*")
    assert "Preciso do orçamento da obra" in found
    assert "orçamentos revisados" in found


def test_exact_phrase(messages_db):
    assert _contents(query='"da obra"') == ["Preciso do orçamento da obra"]


def test_space_separated_non_latin_scripts_use_the_index(messages_db):
    """Arabic, Devanagari and friends put spaces between words, so unicode61
    tokenizes them and they get the full benefit of the index."""
    assert "مرحبا بالعالم" in _contents(query="مرحبا")


@pytest.mark.parametrize("query", ["会議", "資料", "ります"])
def test_unsegmented_scripts_still_match(messages_db, query):
    """unicode61 has no word segmentation for scripts written without spaces, so
    a whole Japanese sentence is one token and a word inside it would never
    match. Those queries stay on the substring scan instead of regressing."""
    assert "会議の資料を送ります" in _contents(query=query)


class TestUnsegmentedScriptDetection:
    @pytest.mark.parametrize("query", ["会議", "ひらがな", "カタカナ", "สวัสดี"])
    def test_detects(self, query):
        assert whatsapp._is_unsegmented_script(query)

    @pytest.mark.parametrize("query", ["orcamento", "مرحبا", "नमस्ते", "Привет"])
    def test_ignores_segmented_scripts(self, query):
        assert not whatsapp._is_unsegmented_script(query)


def test_operator_characters_do_not_raise(messages_db):
    """Literal text containing FTS5 operators must not surface as an error.

    Parentheses, a leading dash and an unbalanced quote are all syntax errors in
    an FTS5 expression; the caller gets results or nothing, never an exception.
    """
    for text in ["obra (casa)", "-orcamento", 'boleto "', "***", "!!!"]:
        assert isinstance(_contents(query=text), list)


def test_relevance_sort_requires_no_extra_setup(messages_db):
    found = _contents(query="orcament*", sort_by="relevance")
    assert found  # ordering is BM25's business; what matters is that it runs
    assert all("rçamento" in c for c in found)


def test_empty_content_is_not_indexed(messages_db):
    """Rows with no text must not appear for any query."""
    assert "" not in _contents(query="obra")


def test_triggers_keep_the_index_in_sync(messages_db):
    """Insert, update and delete after the index exists must all be reflected."""
    _contents(query="obra")  # build the index

    conn = sqlite3.connect(str(messages_db))
    conn.execute(
        "INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me) VALUES (?, ?, ?, ?, ?, ?)",
        ("m10", CHAT, "1234567890", "contrato assinado", "2024-01-15 11:00:00+00:00", 0),
    )
    conn.commit()
    assert _contents(query="contrato") == ["contrato assinado"]

    conn.execute("UPDATE messages SET content = ? WHERE id = ?", ("contrato cancelado", "m10"))
    conn.commit()
    assert _contents(query="assinado") == []
    assert _contents(query="cancelado") == ["contrato cancelado"]

    conn.execute("DELETE FROM messages WHERE id = ?", ("m10",))
    conn.commit()
    conn.close()
    assert _contents(query="cancelado") == []


def test_falls_back_to_substring_scan_without_the_index(messages_db, monkeypatch):
    """A SQLite build without FTS5, or a read-only database, must still search."""
    monkeypatch.setattr(whatsapp, "_ensure_fts_index", lambda conn, path: False)
    found = _contents(query="ana")
    # The fallback is the old substring behaviour, so "semana" matches again.
    assert "Na semana passada foi analisado" in found


def test_unfiltered_listing_is_unchanged(messages_db):
    """No query means no FTS join and no behaviour change."""
    assert len(_contents()) == len(ROWS)


class TestFtsQuote:
    def test_quotes_each_token(self):
        assert whatsapp._fts_quote("obra (casa)") == '"obra" AND "casa"'

    def test_keeps_non_latin_tokens(self):
        assert whatsapp._fts_quote("会議 مرحبا") == '"会議" AND "مرحبا"'

    def test_empty_when_there_is_nothing_to_match(self):
        assert whatsapp._fts_quote("***") == ""
