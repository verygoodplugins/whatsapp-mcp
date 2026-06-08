"""Tests for WhatsApp MCP server functions."""

from datetime import datetime

from whatsapp import Chat, Contact, Message, chat_to_dict, contact_to_dict, msg_to_dict


class TestMessageConversion:
    """Tests for message conversion functions."""

    def test_msg_to_dict_basic(self):
        """Test basic message to dict conversion."""
        msg = Message(
            id="msg123",
            timestamp=datetime(2024, 1, 15, 10, 30, 0),
            sender="1234567890@s.whatsapp.net",
            content="Hello, world!",
            is_from_me=False,
            chat_jid="1234567890@s.whatsapp.net",
            chat_name="John Doe",
            media_type=None,
        )

        result = msg_to_dict(msg, include_sender_name=False)

        assert result["id"] == "msg123"
        assert result["timestamp"] == "2024-01-15T10:30:00"
        assert result["sender_jid"] == "1234567890@s.whatsapp.net"
        assert result["sender_phone"] == "1234567890"
        assert result["content"] == "Hello, world!"
        assert result["is_from_me"] is False
        assert result["chat_jid"] == "1234567890@s.whatsapp.net"
        assert result["chat_name"] == "John Doe"
        assert result["media_type"] is None
        assert result["quoted_message_id"] is None
        assert result["reaction_to_message_id"] is None

    def test_msg_to_dict_from_me(self):
        """Test message from self shows 'Me' as sender."""
        msg = Message(
            id="msg456",
            timestamp=datetime(2024, 1, 15, 10, 30, 0),
            sender="me@s.whatsapp.net",
            content="My message",
            is_from_me=True,
            chat_jid="1234567890@s.whatsapp.net",
        )

        result = msg_to_dict(msg, include_sender_name=True)

        assert result["sender_name"] == "Me"
        assert result["sender_display"] == "Me"

    def test_msg_to_dict_with_media(self):
        """Test message with media type."""
        msg = Message(
            id="msg789",
            timestamp=datetime(2024, 1, 15, 10, 30, 0),
            sender="1234567890@s.whatsapp.net",
            content="",
            is_from_me=False,
            chat_jid="1234567890@s.whatsapp.net",
            media_type="image",
        )

        result = msg_to_dict(msg, include_sender_name=False)

        assert result["media_type"] == "image"
        assert result["reaction_to_message_id"] is None

    def test_msg_to_dict_reaction_exposes_reaction_to_message_id(self):
        """Reaction messages expose the reacted-to message ID via reaction_to_message_id."""
        msg = Message(
            id="react-001",
            timestamp=datetime(2024, 1, 15, 10, 30, 0),
            sender="1234567890@s.whatsapp.net",
            content="👍",
            is_from_me=False,
            chat_jid="1234567890@s.whatsapp.net",
            media_type="reaction",
            filename="3AABCDEF01234567",  # reacted-to message ID stored in filename
        )

        result = msg_to_dict(msg, include_sender_name=False)

        assert result["reaction_to_message_id"] == "3AABCDEF01234567"
        assert result["quoted_message_id"] is None
        assert result["media_type"] == "reaction"

    def test_msg_to_dict_non_reaction_has_null_reaction_to_message_id(self):
        """Non-reaction messages always have reaction_to_message_id as None."""
        msg = Message(
            id="msg-img",
            timestamp=datetime(2024, 1, 15, 10, 30, 0),
            sender="1234567890@s.whatsapp.net",
            content="",
            is_from_me=False,
            chat_jid="1234567890@s.whatsapp.net",
            media_type="image",
            filename="photo.jpg",
        )

        result = msg_to_dict(msg, include_sender_name=False)

        assert result["reaction_to_message_id"] is None

    def test_msg_to_dict_quoted_message_id_present(self):
        """Quoted-reply messages expose quoted_message_id."""
        msg = Message(
            id="reply-001",
            timestamp=datetime(2024, 1, 15, 10, 30, 0),
            sender="1234567890@s.whatsapp.net",
            content="Great point!",
            is_from_me=False,
            chat_jid="1234567890@s.whatsapp.net",
            quoted_message_id="3AORIGINAL0000001",
        )

        result = msg_to_dict(msg, include_sender_name=False)

        assert result["quoted_message_id"] == "3AORIGINAL0000001"
        assert result["reaction_to_message_id"] is None

    def test_msg_to_dict_quoted_message_id_absent(self):
        """Plain messages have quoted_message_id as None."""
        msg = Message(
            id="plain-001",
            timestamp=datetime(2024, 1, 15, 10, 30, 0),
            sender="1234567890@s.whatsapp.net",
            content="Hello!",
            is_from_me=False,
            chat_jid="1234567890@s.whatsapp.net",
        )

        result = msg_to_dict(msg, include_sender_name=False)

        assert result["quoted_message_id"] is None
        assert result["reaction_to_message_id"] is None

    def test_msg_to_dict_reaction_preserves_quoted_message_id(self):
        """Reaction messages still include quoted_message_id if the row has one."""
        msg = Message(
            id="react-quote-001",
            timestamp=datetime(2024, 1, 15, 10, 30, 0),
            sender="1234567890@s.whatsapp.net",
            content="👍",
            is_from_me=False,
            chat_jid="1234567890@s.whatsapp.net",
            media_type="reaction",
            filename="3AREACTED0000001",
            quoted_message_id="3AORIGINAL0000001",
        )

        result = msg_to_dict(msg, include_sender_name=False)

        assert result["reaction_to_message_id"] == "3AREACTED0000001"
        assert result["quoted_message_id"] == "3AORIGINAL0000001"


class TestChatConversion:
    """Tests for chat conversion functions."""

    def test_chat_to_dict_dm(self):
        """Test direct message chat conversion."""
        chat = Chat(
            jid="1234567890@s.whatsapp.net",
            name="John Doe",
            last_message_time=datetime(2024, 1, 15, 10, 30, 0),
            last_message="Hello!",
            last_sender="1234567890@s.whatsapp.net",
            last_is_from_me=False,
        )

        result = chat_to_dict(chat)

        assert result["jid"] == "1234567890@s.whatsapp.net"
        assert result["name"] == "John Doe"
        assert result["is_group"] is False
        assert result["last_message_time"] == "2024-01-15T10:30:00"
        assert result["last_message"] == "Hello!"

    def test_chat_to_dict_group(self):
        """Test group chat conversion."""
        chat = Chat(
            jid="123456789@g.us",
            name="Family Group",
            last_message_time=datetime(2024, 1, 15, 10, 30, 0),
        )

        result = chat_to_dict(chat)

        assert result["jid"] == "123456789@g.us"
        assert result["is_group"] is True

    def test_chat_to_dict_no_last_message(self):
        """Test chat without last message time."""
        chat = Chat(
            jid="1234567890@s.whatsapp.net",
            name="Jane Doe",
            last_message_time=None,
        )

        result = chat_to_dict(chat)

        assert result["last_message_time"] is None


class TestContactConversion:
    """Tests for contact conversion functions."""

    def test_contact_to_dict(self):
        """Test contact to dict conversion."""
        contact = Contact(
            phone_number="1234567890",
            name="John Doe",
            jid="1234567890@s.whatsapp.net",
        )

        result = contact_to_dict(contact)

        assert result["phone_number"] == "1234567890"
        assert result["name"] == "John Doe"
        assert result["jid"] == "1234567890@s.whatsapp.net"

    def test_contact_to_dict_no_name(self):
        """Test contact without name."""
        contact = Contact(
            phone_number="9876543210",
            name=None,
            jid="9876543210@s.whatsapp.net",
        )

        result = contact_to_dict(contact)

        assert result["name"] is None
