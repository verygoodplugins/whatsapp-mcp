import json
import os
import os.path
from dataclasses import dataclass
from datetime import datetime
from typing import Any

import requests

import audio

# Configuration via environment variables with sensible defaults
WHATSAPP_API_BASE_URL = os.getenv("WHATSAPP_API_URL", "http://localhost:8080/api")


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


def _api_response_to_message(data: dict) -> Message:
    """Convert an API message response dict to a Message dataclass."""
    return Message(
        timestamp=datetime.fromisoformat(data["timestamp"]),
        sender=data["sender"],
        chat_name=data.get("chat_name"),
        content=data.get("content", ""),
        is_from_me=data.get("is_from_me", False),
        chat_jid=data.get("chat_jid", ""),
        id=data.get("id", ""),
        media_type=data.get("media_type"),
    )


def get_sender_name(sender_jid: str) -> str:
    try:
        response = requests.get(
            f"{WHATSAPP_API_BASE_URL}/sender-name",
            params={"jid": sender_jid},
        )
        if response.status_code == 200:
            result = response.json()
            return result.get("name", sender_jid)
        return sender_jid
    except requests.RequestException as e:
        print(f"API error while getting sender name: {e}")
        return sender_jid


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
    """Get messages matching the specified criteria with optional context."""
    try:
        # Validate date formats before sending to API
        if after:
            try:
                datetime.fromisoformat(after)
            except ValueError:
                raise ValueError(f"Invalid date format for 'after': {after}. Please use ISO-8601 format.")

        if before:
            try:
                datetime.fromisoformat(before)
            except ValueError:
                raise ValueError(f"Invalid date format for 'before': {before}. Please use ISO-8601 format.")

        params: dict[str, Any] = {"limit": limit, "page": page, "sort_by": sort_by}
        if after:
            params["after"] = after
        if before:
            params["before"] = before
        if sender_phone_number:
            params["sender"] = sender_phone_number
        if chat_jid:
            params["chat_jid"] = chat_jid
        if query:
            params["query"] = query

        response = requests.get(f"{WHATSAPP_API_BASE_URL}/messages", params=params)
        response.raise_for_status()
        messages_data = response.json()

        result = [_api_response_to_message(m) for m in messages_data]

        if include_context and result:
            # Add context for each message, deduplicated by message ID
            seen_ids = set()
            messages_with_context = []
            for msg in result:
                context = get_message_context(msg.id, context_before, context_after)
                for ctx_msg in context.before:
                    if ctx_msg.id not in seen_ids:
                        seen_ids.add(ctx_msg.id)
                        messages_with_context.append(ctx_msg)
                if context.message.id not in seen_ids:
                    seen_ids.add(context.message.id)
                    messages_with_context.append(context.message)
                for ctx_msg in context.after:
                    if ctx_msg.id not in seen_ids:
                        seen_ids.add(ctx_msg.id)
                        messages_with_context.append(ctx_msg)

            return [msg_to_dict(msg) for msg in messages_with_context]

        # Return messages without context
        return [msg_to_dict(msg) for msg in result]

    except requests.RequestException as e:
        print(f"API error: {e}")
        return []


def get_message_context(message_id: str, before: int = 5, after: int = 5) -> MessageContext:
    """Get context around a specific message."""
    try:
        response = requests.get(
            f"{WHATSAPP_API_BASE_URL}/messages",
            params={"message_id": message_id, "before_count": before, "after_count": after},
        )
        response.raise_for_status()
        data = response.json()

        if "error" in data:
            raise ValueError(data["error"])

        target_message = _api_response_to_message(data["message"])

        before_messages = [_api_response_to_message(m) for m in data.get("before", [])]
        after_messages = [_api_response_to_message(m) for m in data.get("after", [])]

        return MessageContext(message=target_message, before=before_messages, after=after_messages)

    except requests.RequestException as e:
        print(f"API error: {e}")
        raise


def list_chats(
    query: str | None = None,
    limit: int = 20,
    page: int = 0,
    include_last_message: bool = True,
    sort_by: str = "last_active",
) -> list[dict[str, Any]]:
    """Get chats matching the specified criteria."""
    try:
        params: dict[str, Any] = {
            "limit": limit,
            "page": page,
            "include_last_message": "true" if include_last_message else "false",
            "sort_by": sort_by,
        }
        if query:
            params["query"] = query

        response = requests.get(f"{WHATSAPP_API_BASE_URL}/chats", params=params)
        response.raise_for_status()
        return response.json()

    except requests.RequestException as e:
        print(f"API error: {e}")
        return []


def search_contacts(query: str) -> list[dict[str, Any]]:
    """Search contacts by name or phone number."""
    try:
        response = requests.get(
            f"{WHATSAPP_API_BASE_URL}/contacts",
            params={"query": query},
        )
        response.raise_for_status()
        return response.json()

    except requests.RequestException as e:
        print(f"API error: {e}")
        return []


def get_contact_chats(jid: str, limit: int = 20, page: int = 0) -> list[dict[str, Any]]:
    """Get all chats involving the contact."""
    try:
        response = requests.get(
            f"{WHATSAPP_API_BASE_URL}/chats",
            params={"contact_jid": jid, "limit": limit, "page": page},
        )
        response.raise_for_status()
        return response.json()

    except requests.RequestException as e:
        print(f"API error: {e}")
        return []


def get_last_interaction(jid: str) -> dict[str, Any] | None:
    """Get most recent message involving the contact."""
    try:
        response = requests.get(
            f"{WHATSAPP_API_BASE_URL}/messages",
            params={"jid": jid},
        )
        if response.status_code == 404:
            return None
        response.raise_for_status()
        data = response.json()

        if "error" in data:
            return None

        message = _api_response_to_message(data)
        return msg_to_dict(message)

    except requests.RequestException as e:
        print(f"API error: {e}")
        return None


def get_chat(chat_jid: str, include_last_message: bool = True) -> dict[str, Any] | None:
    """Get chat metadata by JID."""
    try:
        params: dict[str, Any] = {
            "jid": chat_jid,
            "include_last_message": "true" if include_last_message else "false",
        }
        response = requests.get(f"{WHATSAPP_API_BASE_URL}/chats", params=params)
        if response.status_code == 404:
            return None
        response.raise_for_status()
        return response.json()

    except requests.RequestException as e:
        print(f"API error: {e}")
        return None


def get_direct_chat_by_contact(sender_phone_number: str) -> dict[str, Any] | None:
    """Get chat metadata by sender phone number."""
    try:
        response = requests.get(
            f"{WHATSAPP_API_BASE_URL}/chats",
            params={"phone": sender_phone_number},
        )
        if response.status_code == 404:
            return None
        response.raise_for_status()
        return response.json()

    except requests.RequestException as e:
        print(f"API error: {e}")
        return None


def send_message(recipient: str, message: str) -> tuple[bool, str]:
    try:
        # Validate input
        if not recipient:
            return False, "Recipient must be provided"

        url = f"{WHATSAPP_API_BASE_URL}/send"
        payload = {
            "recipient": recipient,
            "message": message,
        }

        response = requests.post(url, json=payload)

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

        if not media_path:
            return False, "Media path must be provided"

        if not os.path.isfile(media_path):
            return False, f"Media file not found: {media_path}"

        url = f"{WHATSAPP_API_BASE_URL}/send"
        payload = {"recipient": recipient, "media_path": media_path}

        response = requests.post(url, json=payload)

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

        response = requests.post(url, json=payload)

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


def download_media(message_id: str, chat_jid: str) -> str | None:
    """Download media from a message and return the local file path.

    Args:
        message_id: The ID of the message containing the media
        chat_jid: The JID of the chat containing the message

    Returns:
        The local file path if download was successful, None otherwise
    """
    try:
        url = f"{WHATSAPP_API_BASE_URL}/download"
        payload = {"message_id": message_id, "chat_jid": chat_jid}

        response = requests.post(url, json=payload)

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
