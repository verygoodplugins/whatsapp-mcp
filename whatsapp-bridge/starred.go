package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// Starred messages, as set on the phone or through /api/star.
//
// Stars sync between devices as app-state mutations (regular_high), the same
// channel as mute, so syncChatStateOnce's full snapshot also replays every
// existing star. They live in their own table rather than a messages column:
// a star can point at a message this bridge never stored (older than its
// history), and it must still land when that message arrives later. Unstars
// are kept as starred = 0 rows so a late, older star can't resurrect one.

func ensureStarredMessagesTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS starred_messages (
		chat_jid TEXT NOT NULL,
		message_id TEXT NOT NULL,
		starred INTEGER NOT NULL,
		starred_ts INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (chat_jid, message_id)
	)`)
	if err != nil {
		return fmt.Errorf("failed to create starred_messages: %w", err)
	}
	return nil
}

// SetMessageStarred records a message's star state. Like setChatFlag, a write
// only lands if it is at least as new as the stored one.
func (store *MessageStore) SetMessageStarred(chatJID, messageID string, starred bool, at time.Time) error {
	_, err := store.db.Exec(
		`INSERT INTO starred_messages (chat_jid, message_id, starred, starred_ts)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(chat_jid, message_id) DO UPDATE SET
			starred = excluded.starred,
			starred_ts = excluded.starred_ts
		WHERE excluded.starred_ts >= starred_messages.starred_ts`,
		chatJID, messageID, boolInt(starred), millis(at),
	)
	return err
}

// errStarMessageNotStored means /api/star was asked about a message the
// bridge has no row for, so it can't tell the sender or direction.
var errStarMessageNotStored = errors.New("message not found")

// StoredMessageAuthor returns the stored sender (bare user) and direction of
// a message: the two parts of a star's app-state key the caller doesn't give.
func (store *MessageStore) StoredMessageAuthor(chatJID, messageID string) (sender string, isFromMe bool, err error) {
	err = store.db.QueryRow(
		`SELECT sender, is_from_me FROM messages WHERE id = ? AND chat_jid = ?`, messageID, chatJID,
	).Scan(&sender, &isFromMe)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("%w: %q in chat %q", errStarMessageNotStored, messageID, chatJID)
	}
	return sender, isFromMe, err
}

// starParticipant returns the sender user for a star's participant slot, or
// "" when the slot must be "0": a DM, or our own message. appstate.BuildStar
// writes "0" when the sender equals the chat, so the caller passes the chat
// as sender then. Only someone else's message in a group names its sender.
func starParticipant(chat types.JID, storedSender string, isFromMe bool) string {
	if isFromMe || chat.Server != types.GroupServer {
		return ""
	}
	return bareSenderUser(storedSender)
}

// handleStarEvent stores a star / unstar synced from another device.
func handleStarEvent(client *whatsmeow.Client, store *MessageStore, evt *events.Star, logger waLog.Logger) {
	jid := appStateChatJID(client, evt.ChatJID)
	if err := store.SetMessageStarred(jid, evt.MessageID, evt.Action.GetStarred(), evt.Timestamp); err != nil {
		logger.Warnf("Failed to store star state for %s in %s: %v", evt.MessageID, jid, err)
	}
}

// sendStar stars or unstars a stored message on every linked device, then
// records it locally so list_messages reflects it without waiting for the
// patch to sync back. JIDs are resolved PN -> LID like /api/mark-read does,
// since the phone keys migrated contacts by LID.
func sendStar(ctx context.Context, client *whatsmeow.Client, store *MessageStore, req StarRequest) error {
	storedSender, isFromMe, err := store.StoredMessageAuthor(req.ChatJID, req.MessageID)
	if err != nil {
		return err
	}
	chat, err := resolveRecipientJID(client, req.ChatJID)
	if err != nil {
		return fmt.Errorf("invalid chat_jid: %w", err)
	}
	sender := chat
	if user := starParticipant(chat, storedSender, isFromMe); user != "" {
		if sender, err = groupStarSender(ctx, client, chat, user); err != nil {
			return fmt.Errorf("invalid stored sender: %w", err)
		}
	}
	patch := appstate.BuildStar(chat, sender, req.MessageID, isFromMe, *req.Starred)
	if err := sendAppStateWithResync(ctx, whatsmeowAppState{client}, patch, client.Log.Warnf); err != nil {
		return fmt.Errorf("send star: %w", err)
	}
	if err := store.SetMessageStarred(req.ChatJID, req.MessageID, *req.Starred, time.Now()); err != nil {
		// The phone has it; the echo from the app-state sync will store it.
		fmt.Printf("Warning: failed to store star state for %s: %v\n", req.MessageID, err)
	}
	return nil
}

// groupStarSender returns the JID a group star must name for the message's
// sender. The bridge stores only the sender's bare user, which is the LID
// digits when the phone number was never learned, so it can't be rebuilt as
// a phone JID blindly: that names a user who doesn't exist and the star
// matches nothing on the phone. The group's participant list gives the JID
// in the group's own addressing (LID or phone). A sender who has left falls
// back to the LID store.
func groupStarSender(ctx context.Context, client *whatsmeow.Client, group types.JID, user string) (types.JID, error) {
	info, err := client.GetGroupInfo(ctx, group)
	if err == nil {
		if jid, ok := participantJID(info.Participants, user); ok {
			return jid, nil
		}
	}
	lid := types.NewJID(user, types.HiddenUserServer)
	if pn, err := client.Store.LIDs.GetPNForLID(ctx, lid); err == nil && !pn.IsEmpty() {
		return lid, nil // the stored user is a known LID
	}
	if info != nil && info.AddressingMode == types.AddressingModeLID {
		// Unknown either way, in a LID group: a stored bare user here is
		// a LID that never resolved to a phone number.
		return lid, nil
	}
	return resolveRecipientJID(client, user)
}

// participantJID finds the participant whose LID or phone number has the
// given user, and returns the JID the group addresses them by.
func participantJID(participants []types.GroupParticipant, user string) (types.JID, bool) {
	for _, p := range participants {
		if p.JID.User == user || p.LID.User == user || p.PhoneNumber.User == user {
			return p.JID.ToNonAD(), true
		}
	}
	return types.JID{}, false
}
