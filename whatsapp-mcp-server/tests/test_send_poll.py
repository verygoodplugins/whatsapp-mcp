"""Tests for send_poll input validation."""

from unittest.mock import patch

from whatsapp import send_poll


class TestSendPollValidation:
    def test_missing_recipient(self):
        ok, msg = send_poll("", "Q?", ["a", "b"])
        assert not ok
        assert "Recipient" in msg

    def test_missing_name(self):
        ok, msg = send_poll("123@s.whatsapp.net", "   ", ["a", "b"])
        assert not ok
        assert "Poll name" in msg

    def test_too_few_options(self):
        ok, msg = send_poll("123@s.whatsapp.net", "Q?", ["only"])
        assert not ok
        assert "two" in msg.lower()

    def test_too_many_options(self):
        ok, msg = send_poll("123@s.whatsapp.net", "Q?", [str(i) for i in range(13)])
        assert not ok
        assert "12" in msg

    def test_empty_option(self):
        ok, msg = send_poll("123@s.whatsapp.net", "Q?", ["a", "  "])
        assert not ok
        assert "must not be empty" in msg

    def test_selectable_count_out_of_range(self):
        ok, msg = send_poll("123@s.whatsapp.net", "Q?", ["a", "b"], selectable_option_count=5)
        assert not ok
        assert "selectable_option_count" in msg

    def test_selectable_count_zero_rejected(self):
        ok, msg = send_poll("123@s.whatsapp.net", "Q?", ["a", "b"], selectable_option_count=0)
        assert not ok
        assert "selectable_option_count" in msg

    def test_valid_request_calls_bridge(self):
        with patch("whatsapp.requests.post") as mock_post:
            mock_post.return_value.status_code = 200
            mock_post.return_value.json.return_value = {"success": True, "message": "Poll sent to 123"}

            ok, msg = send_poll("123@s.whatsapp.net", "Lunch?", ["pizza", "salad"], selectable_option_count=1)

            assert ok
            assert msg == "Poll sent to 123"
            mock_post.assert_called_once()
            call = mock_post.call_args
            assert call.kwargs["json"] == {
                "recipient": "123@s.whatsapp.net",
                "name": "Lunch?",
                "options": ["pizza", "salad"],
                "selectable_option_count": 1,
            }
            assert call.args[0].endswith("/send/poll")
