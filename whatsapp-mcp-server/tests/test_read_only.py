"""This fork is read-only, and that has to be enforced, not promised.

An MCP client decides what it can do from the tool list the server advertises,
so a write tool that exists is a write tool that can fire — whatever the
instructions around it say. These tests fail if one comes back.
"""

import asyncio

import pytest

import main
import whatsapp

# Anything that would change state on WhatsApp, including the ones other people
# can see without a message being sent: reactions, read receipts, typing.
WRITE_TOOLS = {
    "send_message",
    "send_file",
    "send_audio_message",
    "send_reaction",
    "mark_messages_read",
}

READ_TOOLS = {
    "search_contacts",
    "get_contact",
    "list_messages",
    "list_chats",
    "get_chat",
    "get_direct_chat_by_contact",
    "get_contact_chats",
    "get_last_interaction",
    "get_message_context",
    "download_media",
    "view_media",
    "transcribe_audio",
}


@pytest.fixture
def tool_names():
    tools = asyncio.run(main.mcp.list_tools())
    return {tool.name for tool in tools}


def test_no_write_tools_are_advertised(tool_names):
    assert not (tool_names & WRITE_TOOLS), f"write tools are exposed: {sorted(tool_names & WRITE_TOOLS)}"


def test_read_tools_are_all_advertised(tool_names):
    assert READ_TOOLS <= tool_names, f"missing read tools: {sorted(READ_TOOLS - tool_names)}"


@pytest.mark.parametrize("name", sorted(WRITE_TOOLS))
def test_write_helpers_are_gone_from_the_client_module(name):
    assert not hasattr(whatsapp, name), f"whatsapp.{name} still exists; the capability must not be in the codebase"
