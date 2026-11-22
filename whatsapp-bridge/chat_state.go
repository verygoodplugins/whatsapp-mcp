package main

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// Chat app state: archive, mute and mark-as-unread, as set on the phone.
//
// WhatsApp syncs these between devices as app-state mutations, not messages.
// Each flag is stored with the unix-millisecond time of the mutation that set
// it (`*_ts`), and a write only lands if it is at least as new as the stored
// one, so out-of-order patches and stale history-sync data can't roll a flag
// back. History sync carries no mutation time and writes with ts 0, so it
// only fills in chats app state never reported on.

// chatFlag names a value column and its timestamp column. Only these
// constants are ever interpolated into SQL.
type chatFlag struct{ value, ts string }

var (
	chatFlagArchived     = chatFlag{"archived", "archived_ts"}
	chatFlagMutedUntil   = chatFlag{"muted_until", "muted_ts"}
	chatFlagMarkedUnread = chatFlag{"marked_unread", "marked_unread_ts"}
)

// ensureChatStateColumns adds the app-state columns; part of
// ensureMessageStoreSchema.
func ensureChatStateColumns(db *sql.DB) error {
	for _, flag := range []chatFlag{chatFlagArchived, chatFlagMutedUntil, chatFlagMarkedUnread} {
		if err := ensureColumn(db, "chats", flag.value, "INTEGER"); err != nil {
			return fmt.Errorf("failed to ensure chats.%s column: %w", flag.value, err)
		}
		if err := ensureColumn(db, "chats", flag.ts, "INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("failed to ensure chats.%s column: %w", flag.ts, err)
		}
	}
	return nil
}

// setChatFlag upserts one flag. Like MarkChatRead it inserts only its own
// columns, so an app-state event for a chat with no messages yet doesn't
// fabricate name/last_message_time.
func (store *MessageStore) setChatFlag(jid string, flag chatFlag, value int64, atMillis int64) error {
	_, err := store.db.Exec(fmt.Sprintf(
		`INSERT INTO chats (jid, %[1]s, %[2]s)
		VALUES (?, ?, ?)
		ON CONFLICT(jid) DO UPDATE SET
			%[1]s = excluded.%[1]s,
			%[2]s = excluded.%[2]s
		WHERE excluded.%[2]s >= chats.%[2]s`, flag.value, flag.ts),
		jid, value, atMillis,
	)
	return err
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// SetChatArchived records the chat's archive state.
func (store *MessageStore) SetChatArchived(jid string, archived bool, at time.Time) error {
	return store.setChatFlag(jid, chatFlagArchived, boolInt(archived), millis(at))
}

// SetChatMarkedUnread records whether the chat is manually marked unread.
func (store *MessageStore) SetChatMarkedUnread(jid string, marked bool, at time.Time) error {
	return store.setChatFlag(jid, chatFlagMarkedUnread, boolInt(marked), millis(at))
}

// SetChatMutedUntil records the mute end in unix milliseconds, as WhatsApp
// reports it: -1 = muted indefinitely, 0 = not muted.
func (store *MessageStore) SetChatMutedUntil(jid string, mutedUntilMillis int64, at time.Time) error {
	return store.setChatFlag(jid, chatFlagMutedUntil, mutedUntilMillis, millis(at))
}

// muteEndMillis maps a MuteAction to the stored muted_until value.
func muteEndMillis(muted bool, endTimestamp int64) int64 {
	if !muted {
		return 0
	}
	if endTimestamp == 0 {
		return -1 // muted with no end: "always"
	}
	return endTimestamp
}

// appStateChatJID maps an app-state target to the chat JID messages are
// stored under. App-state patches carry no alt JIDs, so LID chats resolve
// through the whatsmeow LID store only.
func appStateChatJID(client *whatsmeow.Client, jid types.JID) string {
	return resolveLIDChat(client, jid, types.EmptyJID, types.EmptyJID, false).String()
}

// handleChatStateEvent stores archive / mute / mark-as-unread app-state
// events. It reports whether evt was one of them.
func handleChatStateEvent(client *whatsmeow.Client, store *MessageStore, evt interface{}, logger waLog.Logger) bool {
	switch v := evt.(type) {
	case *events.Archive:
		jid := appStateChatJID(client, v.JID)
		if err := store.SetChatArchived(jid, v.Action.GetArchived(), v.Timestamp); err != nil {
			logger.Warnf("Failed to store archive state for %s: %v", jid, err)
		}
	case *events.Mute:
		jid := appStateChatJID(client, v.JID)
		until := muteEndMillis(v.Action.GetMuted(), v.Action.GetMuteEndTimestamp())
		if err := store.SetChatMutedUntil(jid, until, v.Timestamp); err != nil {
			logger.Warnf("Failed to store mute state for %s: %v", jid, err)
		}
	case *events.MarkChatAsRead:
		jid := appStateChatJID(client, v.JID)
		read := v.Action.GetRead()
		if err := store.SetChatMarkedUnread(jid, !read, v.Timestamp); err != nil {
			logger.Warnf("Failed to store marked-unread state for %s: %v", jid, err)
		}
		if read {
			// "Mark as read" on the phone reads the whole chat up to its last
			// message; the monotonic merge keeps a stale full-sync value from
			// moving the marker back.
			readAt := v.Timestamp
			if ts := v.Action.GetMessageRange().GetLastMessageTimestamp(); ts > 0 {
				readAt = time.Unix(ts, 0)
			}
			if err := store.MarkChatRead(jid, readAt); err != nil {
				logger.Warnf("Failed to mark chat %s read: %v", jid, err)
			}
		}
	default:
		return false
	}
	return true
}

// storeHistoryChatState backfills archive and marked-unread from a history
// sync conversation. Sparse chunks omit the fields (the getters then return
// false), so only fields actually present are written. Mute is left to the
// app-state sync: its history-sync unit isn't documented.
func storeHistoryChatState(store *MessageStore, chatJID string, conversation *waHistorySync.Conversation, logger waLog.Logger) {
	if conversation.Archived != nil {
		if err := store.SetChatArchived(chatJID, conversation.GetArchived(), time.Time{}); err != nil {
			logger.Warnf("Failed to backfill archive state for %s: %v", chatJID, err)
		}
	}
	if conversation.MarkedAsUnread != nil {
		if err := store.SetChatMarkedUnread(chatJID, conversation.GetMarkedAsUnread(), time.Time{}); err != nil {
			logger.Warnf("Failed to backfill marked-unread state for %s: %v", chatJID, err)
		}
	}
}

// chatStatePatches hold the archive / markChatAsRead (regular_low) and mute
// (regular_high) mutations.
var chatStatePatches = []appstate.WAPatchName{appstate.WAPatchRegularLow, appstate.WAPatchRegularHigh}

// chatStateSynced is set once a full chat app-state sync has succeeded in
// this process, so reconnects don't repeat it.
var chatStateSynced atomic.Bool

// syncChatStateOnce re-downloads the chat app-state snapshots so the bridge
// learns the current archive / mute / marked-unread state of every chat,
// including what was set before this bridge version stored it. Incremental
// patches only carry changes; without this a long-linked device never sees
// state set before the upgrade. The client must have
// EmitAppStateEventsOnFullSync set, or the snapshot emits no events.
// Failures leave the flag unset so the next connect retries.
func syncChatStateOnce(ctx context.Context, client *whatsmeow.Client, logger waLog.Logger) {
	if chatStateSynced.Load() {
		return
	}
	for _, name := range chatStatePatches {
		if err := client.FetchAppState(ctx, name, true, false); err != nil {
			logger.Warnf("Chat app-state sync of %s failed (will retry on next connect): %v", name, err)
			return
		}
	}
	chatStateSynced.Store(true)
	logger.Infof("Chat app-state (archive/mute/marked-unread) synced")
}
