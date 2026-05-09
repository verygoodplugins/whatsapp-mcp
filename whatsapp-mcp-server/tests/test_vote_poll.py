"""Tests for vote_poll input validation and bridge call shape."""

from unittest.mock import patch

from whatsapp import vote_poll


class TestVotePollValidation:
    def test_missing_message_id(self):
        ok, msg = vote_poll("", "123@s.whatsapp.net", ["a"])
        assert not ok
        assert "poll_message_id" in msg

    def test_missing_chat_jid(self):
        ok, msg = vote_poll("MSG123", "", ["a"])
        assert not ok
        assert "poll_chat_jid" in msg

    def test_blank_option(self):
        ok, msg = vote_poll("MSG123", "123@s.whatsapp.net", ["a", "  "])
        assert not ok
        assert "must not be empty" in msg

    def test_empty_selection_clears_vote(self):
        # An empty list is the explicit way to clear a vote — must not be
        # rejected client-side; the bridge handles it.
        with patch("whatsapp.requests.post") as mock_post:
            mock_post.return_value.status_code = 200
            mock_post.return_value.json.return_value = {"success": True, "message": "Cleared vote on poll"}

            ok, _ = vote_poll("MSG123", "123@s.whatsapp.net", [])

            assert ok
            assert mock_post.call_args.kwargs["json"]["selected_options"] == []

    def test_valid_request_calls_bridge(self):
        with patch("whatsapp.requests.post") as mock_post:
            mock_post.return_value.status_code = 200
            mock_post.return_value.json.return_value = {"success": True, "message": "Voted on poll (1 selection(s))"}

            ok, msg = vote_poll("MSG123", "123@s.whatsapp.net", ["pizza"])

            assert ok
            assert "Voted" in msg
            mock_post.assert_called_once()
            call = mock_post.call_args
            assert call.kwargs["json"] == {
                "poll_message_id": "MSG123",
                "poll_chat_jid": "123@s.whatsapp.net",
                "selected_options": ["pizza"],
            }
            assert call.args[0].endswith("/vote/poll")

    def test_bridge_error_propagates(self):
        with patch("whatsapp.requests.post") as mock_post:
            mock_post.return_value.status_code = 500
            mock_post.return_value.text = "Poll not found in local store."

            ok, msg = vote_poll("UNKNOWN", "123@s.whatsapp.net", ["pizza"])

            assert not ok
            assert "500" in msg
