import sqlite3

import pytest

import owner_policy


@pytest.fixture
def policy_file(tmp_path, monkeypatch):
    tmp_path.chmod(0o700)
    path = tmp_path / "policy.json"
    policy = owner_policy.initial()
    policy.update(access_enabled=True, allowed_groups=["123-456@g.us"])
    owner_policy.atomic_write(path, policy)
    monkeypatch.setenv("WHATSAPP_POLICY_FILE", str(path))
    return path


def test_atomic_cli_revocation_expansion_and_rollback(policy_file):
    args = ["--policy", str(policy_file)]
    assert owner_policy.cli(args + ["groups", "555@g.us"]) == 2
    assert owner_policy.load()["allowed_groups"] == ["123-456@g.us"]
    assert owner_policy.cli(args + ["--dry-run", "groups", "555@g.us"]) == 0
    assert owner_policy.load()["allowed_groups"] == ["123-456@g.us"]
    assert owner_policy.cli(args + ["revoke"]) == 0
    assert not owner_policy.load()["access_enabled"]
    assert owner_policy.cli(args + ["rollback"]) == 2
    assert owner_policy.cli(args + ["--confirm", "rollback"]) == 0
    assert owner_policy.load()["access_enabled"]
    assert owner_policy.cli(args + ["--confirm", "capability", "media", "on"]) == 2
    assert not owner_policy.load()["capabilities"]["media"]
    assert policy_file.stat().st_mode & 0o777 == 0o600


@pytest.mark.parametrize(
    "group", ["%", "123@s.whatsapp.net", "123@lid", "status@broadcast", "１２３@g.us", "123@g.us "]
)
def test_exact_group_validation(group):
    policy = owner_policy.initial()
    policy["allowed_groups"] = [group]
    with pytest.raises(owner_policy.PolicyError):
        owner_policy.validate(policy)


def test_reject_permissions_duplicates_and_symlinks(policy_file):
    policy_file.chmod(0o644)
    with pytest.raises(owner_policy.PolicyError):
        owner_policy.load()
    policy_file.chmod(0o600)
    data = policy_file.read_text().replace('"version": 1', '"version": 1, "version": 1')
    policy_file.write_text(data)
    with pytest.raises(owner_policy.PolicyError):
        owner_policy.load()
    link = policy_file.with_name("link.json")
    link.symlink_to(policy_file)
    with pytest.raises(OSError):
        owner_policy.load(link)


@pytest.fixture
def reader(policy_file, monkeypatch):
    import readonly_main

    path = policy_file.parent / "messages.db"
    connection = sqlite3.connect(path)
    connection.executescript("""
        CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TEXT);
        CREATE TABLE messages (id TEXT,chat_jid TEXT,sender TEXT,content TEXT,timestamp TEXT,
          is_from_me BOOLEAN,media_type TEXT,quoted_message_id TEXT, PRIMARY KEY(id,chat_jid));
    """)
    for jid in ["999@g.us", "123-456@g.us", "111@s.whatsapp.net", "status@broadcast"]:
        connection.execute("INSERT INTO chats VALUES (?,?,?)", (jid, "Untrusted name", "2026-01-01T12:00:00"))
        for message_id in ["before", "duplicate", "after"]:
            connection.execute(
                "INSERT INTO messages VALUES (?,?,?,?,?,0,'','')",
                (message_id, jid, "same-sender", jid, "2026-01-01T12:00:00"),
            )
    connection.commit()
    connection.close()
    path.chmod(0o600)
    monkeypatch.setenv("WHATSAPP_DB_PATH", str(path))
    return readonly_main


def test_reader_matrix_and_composite_context(reader):
    assert [row["chat_jid"] for row in reader.list_chats()["items"]] == ["123-456@g.us"]
    for jid in ["999@g.us", "111@s.whatsapp.net", "status@broadcast", "%", ""]:
        for tool in [reader.get_chat, reader.list_messages]:
            with pytest.raises(ValueError, match="unavailable"):
                tool(jid)
        with pytest.raises(ValueError, match="unavailable"):
            reader.get_message_context(jid, "duplicate")
    result = reader.get_message_context("123-456@g.us", "duplicate")
    assert len(result["items"]) == 3
    assert all(row["chat_jid"] == "123-456@g.us" for row in result["items"])
    assert result["source"] == "untrusted_whatsapp_data"


def test_revocation_and_database_write_denied(reader, policy_file):
    with reader.database() as connection:
        with pytest.raises(sqlite3.DatabaseError):
            connection.execute("DELETE FROM messages")
        with pytest.raises(sqlite3.DatabaseError):
            connection.execute("ATTACH DATABASE ':memory:' AS bypass")
    rows = reader.query("SELECT chat_jid FROM messages", ())
    owner_policy.cli(["--policy", str(policy_file), "revoke"])
    assert reader.response(rows)["items"] == []
    assert reader.list_chats()["items"] == []


@pytest.mark.parametrize("value", [-1, 0, 101, True])
def test_negative_and_excessive_limits(reader, value):
    with pytest.raises(ValueError):
        reader.list_messages("123-456@g.us", limit=value)


def test_response_bound_and_transport(reader, monkeypatch):
    reader.query("SELECT id FROM messages", ())
    huge = {"chat_jid": "123-456@g.us", "content": "x" * 100000}
    assert reader.response([huge])["truncated"]
    monkeypatch.setenv("WHATSAPP_MCP_TRANSPORT", "http")
    with pytest.raises(ValueError, match="stdio"):
        reader.run()


@pytest.mark.asyncio
async def test_only_four_tools(reader):
    tools = await reader.mcp.list_tools()
    assert {tool.name for tool in tools} == {"list_chats", "get_chat", "list_messages", "get_message_context"}
    for name in ["send_message", "download_media", "transcribe_audio", "get_contact", "mark_messages_read"]:
        with pytest.raises(Exception):
            await reader.mcp.call_tool(name, {})
