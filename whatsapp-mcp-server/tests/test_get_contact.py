import main as mcp_main


def test_get_contact_normalizes_phone_number(monkeypatch):
    def fake_get_chat(jid: str, include_last_message: bool = True):
        assert include_last_message is False
        return {"jid": jid, "name": "John Doe"}

    monkeypatch.setattr(mcp_main, "whatsapp_get_chat", fake_get_chat)
    monkeypatch.setattr(mcp_main, "whatsapp_get_sender_name", lambda jid: jid)

    result = mcp_main.get_contact(identifier="12025551234")

    assert result["jid"] == "12025551234@s.whatsapp.net"
    assert result["is_lid"] is False
    assert result["phone_number"] == "12025551234"
    assert result["lid"] is None
    assert result["name"] == "John Doe"
    assert result["display_name"] == "John Doe"
    assert result["resolved"] is True


def test_get_contact_normalizes_lid(monkeypatch):
    def fake_get_chat(jid: str, include_last_message: bool = True):
        assert include_last_message is False
        if jid.endswith("@s.whatsapp.net"):
            return None
        return {"jid": jid, "name": "Vicky"}

    monkeypatch.setattr(mcp_main, "whatsapp_get_chat", fake_get_chat)
    monkeypatch.setattr(mcp_main, "whatsapp_get_sender_name", lambda jid: jid)

    result = mcp_main.get_contact(identifier="184125298348272")

    assert result["jid"] == "184125298348272@lid"
    assert result["is_lid"] is True
    assert result["phone_number"] is None
    assert result["lid"] == "184125298348272"
    assert result["name"] == "Vicky"
    assert result["display_name"] == "Vicky"
    assert result["resolved"] is True


def test_get_contact_unresolved_falls_back_to_jid_user(monkeypatch):
    monkeypatch.setattr(mcp_main, "whatsapp_get_chat", lambda *args, **kwargs: None)
    monkeypatch.setattr(mcp_main, "whatsapp_get_sender_name", lambda jid: jid)

    result = mcp_main.get_contact(identifier="184125298348272@lid")

    assert result["jid"] == "184125298348272@lid"
    assert result["is_lid"] is True
    assert result["resolved"] is False
    assert result["name"] == "184125298348272"


def test_get_contact_backward_compatible_phone_number_param(monkeypatch):
    monkeypatch.setattr(mcp_main, "whatsapp_get_chat", lambda *args, **kwargs: {"name": "John Doe"})
    monkeypatch.setattr(mcp_main, "whatsapp_get_sender_name", lambda jid: jid)

    result = mcp_main.get_contact(phone_number="12025551234")

    assert result["jid"] == "12025551234@s.whatsapp.net"
    assert result["name"] == "John Doe"
