package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func newMCPAccessTestStore(t *testing.T) *MessageStore {
	t.Helper()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory MCP access database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`
		PRAGMA foreign_keys = ON;
		CREATE TABLE chats (
			jid TEXT PRIMARY KEY,
			name TEXT,
			last_message_time TIMESTAMP
		);
		CREATE TABLE messages (
			id TEXT,
			chat_jid TEXT,
			content TEXT,
			timestamp TIMESTAMP,
			PRIMARY KEY (id, chat_jid),
			FOREIGN KEY (chat_jid) REFERENCES chats(jid)
		);
	`); err != nil {
		t.Fatalf("create MCP access test schema: %v", err)
	}
	if err := ensureMCPAccessSchema(db); err != nil {
		t.Fatalf("create MCP access schema: %v", err)
	}

	return &MessageStore{db: db}
}

func seedMCPAccessChat(t *testing.T, store *MessageStore, jid, name string, lastMessageTime *time.Time) {
	t.Helper()

	var timestamp any
	if lastMessageTime != nil {
		timestamp = *lastMessageTime
	}
	if _, err := store.db.Exec(
		"INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		jid, name, timestamp,
	); err != nil {
		t.Fatalf("seed chat %s: %v", jid, err)
	}
}

func seedMCPAccessMessage(t *testing.T, store *MessageStore, id, chatJID, content string, timestamp time.Time) {
	t.Helper()

	if _, err := store.db.Exec(
		"INSERT INTO messages (id, chat_jid, content, timestamp) VALUES (?, ?, ?, ?)",
		id, chatJID, content, timestamp,
	); err != nil {
		t.Fatalf("seed message %s in %s: %v", id, chatJID, err)
	}
}

func findMCPAdminChat(t *testing.T, chats []MCPAdminChat, jid string) MCPAdminChat {
	t.Helper()
	for _, chat := range chats {
		if chat.JID == jid {
			return chat
		}
	}
	t.Fatalf("chat %s not found in admin result", jid)
	return MCPAdminChat{}
}

func mustListMCPAccessChats(t *testing.T, store *MessageStore) []MCPAdminChat {
	t.Helper()
	chats, err := store.ListMCPAccessChats()
	if err != nil {
		t.Fatalf("list MCP access chats: %v", err)
	}
	return chats
}

func TestMCPAccessSchemaDefaultsToDeny(t *testing.T) {
	store := newMCPAccessTestStore(t)
	chatJID := "15551234567@s.whatsapp.net"
	messageTime := time.Unix(1_710_000_000, 0).UTC()
	seedMCPAccessChat(t, store, chatJID, "Alice", &messageTime)
	seedMCPAccessMessage(t, store, "message-1", chatJID, "private", messageTime)

	// Schema setup is idempotent and must not create an implicit allow row.
	if err := ensureMCPAccessSchema(store.db); err != nil {
		t.Fatalf("repeat schema setup: %v", err)
	}

	canSend, err := store.CanMCPSend(chatJID)
	if err != nil {
		t.Fatalf("check default send permission: %v", err)
	}
	if canSend {
		t.Fatal("chat without a permission row must deny sending")
	}

	canRead, err := store.CanMCPReadMessage("message-1", chatJID)
	if err != nil {
		t.Fatalf("check default read permission: %v", err)
	}
	if canRead {
		t.Fatal("chat without a permission row must deny reading")
	}

	chats, err := store.ListMCPAccessChats()
	if err != nil {
		t.Fatalf("list MCP access chats: %v", err)
	}
	chat := findMCPAdminChat(t, chats, chatJID)
	if chat.ReadNew || chat.ReadHistory || chat.CanSend {
		t.Fatalf("default permissions must all be false: %#v", chat)
	}
	if chat.ReadNewSinceUnix != nil || chat.ReadHistoryThroughUnix != nil || chat.PermissionsUpdatedAtUnix != nil {
		t.Fatalf("unconfigured chat must have null permission timestamps: %#v", chat)
	}

	settings, err := store.GetMCPAccessSettings()
	if err != nil {
		t.Fatalf("load default MCP access settings: %v", err)
	}
	if settings.AllowStartNewConversations {
		t.Fatal("starting new conversations must be disabled by default")
	}
	if settings.Revision != 1 {
		t.Fatalf("default MCP access settings revision = %d, want 1", settings.Revision)
	}
}

func TestMCPAccessSettingsUseOptimisticConcurrency(t *testing.T) {
	store := newMCPAccessTestStore(t)
	now := time.Unix(1_715_000_000, 0).UTC()

	if err := store.UpdateMCPAccessSettings(true, 1, now); err != nil {
		t.Fatalf("enable new conversations: %v", err)
	}
	settings, err := store.GetMCPAccessSettings()
	if err != nil {
		t.Fatalf("load updated MCP access settings: %v", err)
	}
	if !settings.AllowStartNewConversations || settings.Revision != 2 ||
		settings.UpdatedAtUnix != now.Unix() {
		t.Fatalf("unexpected updated MCP access settings: %#v", settings)
	}
	if err := store.UpdateMCPAccessSettings(false, 1, now.Add(time.Minute)); !errors.Is(
		err,
		ErrMCPSettingsConflict,
	) {
		t.Fatalf("stale MCP access settings update error = %v, want conflict", err)
	}

	allowed, err := store.CanMCPStartNewConversations()
	if err != nil {
		t.Fatalf("check new conversation setting: %v", err)
	}
	if !allowed {
		t.Fatal("stale update changed the new conversation setting")
	}
}

func TestGrantMCPNewConversationFullAccessCreatesFullyAllowedChat(t *testing.T) {
	store := newMCPAccessTestStore(t)
	now := time.Unix(1_716_000_000, 0).UTC()
	chatJID := "15551239999@s.whatsapp.net"

	exists, err := store.MCPChatExists(chatJID)
	if err != nil {
		t.Fatalf("check new chat before grant: %v", err)
	}
	if exists {
		t.Fatal("new chat unexpectedly exists before grant")
	}
	if err := store.GrantMCPNewConversationFullAccess(chatJID, now); err != nil {
		t.Fatalf("grant full access to new conversation: %v", err)
	}

	exists, err = store.MCPChatExists(chatJID)
	if err != nil {
		t.Fatalf("check new chat after grant: %v", err)
	}
	if !exists {
		t.Fatal("new chat was not created by grant")
	}
	canSend, err := store.CanMCPSend(chatJID)
	if err != nil {
		t.Fatalf("check new chat send permission: %v", err)
	}
	if !canSend {
		t.Fatal("new chat must allow sending after a successful first message")
	}
	chat := findMCPAdminChat(t, mustListMCPAccessChats(t, store), chatJID)
	if !chat.ReadNew || !chat.ReadHistory || !chat.CanSend {
		t.Fatalf("new conversation must gain full access: %#v", chat)
	}
	if chat.ReadNewSinceUnix == nil || *chat.ReadNewSinceUnix != now.Unix()+1 ||
		chat.ReadHistoryThroughUnix == nil || *chat.ReadHistoryThroughUnix != now.Unix()-1 {
		t.Fatalf("new conversation has unexpected read boundaries: %#v", chat)
	}
}

func TestGrantMCPNewConversationFullAccessPreservesConcurrentExplicitDeny(t *testing.T) {
	store := newMCPAccessTestStore(t)
	now := time.Unix(1_717_000_000, 0).UTC()
	chatJID := "15551238888@s.whatsapp.net"
	seedMCPAccessChat(t, store, chatJID, "Explicit deny", nil)
	if err := store.UpdateMCPChatPermissions(
		[]MCPChatPermissionUpdate{{ChatJID: chatJID}},
		now,
	); err != nil {
		t.Fatalf("store explicit deny: %v", err)
	}

	if err := store.GrantMCPNewConversationFullAccess(chatJID, now.Add(time.Second)); err != nil {
		t.Fatalf("record concurrent new conversation grant: %v", err)
	}
	canSend, err := store.CanMCPSend(chatJID)
	if err != nil {
		t.Fatalf("check preserved explicit deny: %v", err)
	}
	if canSend {
		t.Fatal("automatic new-conversation grant overrode an explicit deny")
	}
	chat := findMCPAdminChat(t, mustListMCPAccessChats(t, store), chatJID)
	if chat.ReadNew || chat.ReadHistory || chat.CanSend {
		t.Fatalf("automatic full-access grant overrode an explicit deny: %#v", chat)
	}
}

func TestMCPAccessIndependentReadWindows(t *testing.T) {
	const (
		newOnlyJID     = "15550000001@s.whatsapp.net"
		historyOnlyJID = "15550000002@s.whatsapp.net"
	)
	store := newMCPAccessTestStore(t)
	now := time.Unix(1_720_000_000, 0).UTC()

	seedMCPAccessChat(t, store, newOnlyJID, "New only", nil)
	seedMCPAccessChat(t, store, historyOnlyJID, "History only", nil)
	for _, chatJID := range []string{newOnlyJID, historyOnlyJID} {
		seedMCPAccessMessage(t, store, "past", chatJID, "past", now.Add(-time.Hour))
		seedMCPAccessMessage(t, store, "boundary", chatJID, "boundary", now)
		seedMCPAccessMessage(t, store, "future", chatJID, "future", now.Add(time.Hour))
	}

	err := store.UpdateMCPChatPermissions([]MCPChatPermissionUpdate{
		{ChatJID: newOnlyJID, ReadNew: true},
		{ChatJID: historyOnlyJID, ReadHistory: true},
	}, now)
	if err != nil {
		t.Fatalf("set independent read windows: %v", err)
	}

	cases := []struct {
		name    string
		chatJID string
		id      string
		want    bool
	}{
		{"new window denies past", newOnlyJID, "past", false},
		{"new window denies ambiguous grant second", newOnlyJID, "boundary", false},
		{"new window allows future", newOnlyJID, "future", true},
		{"history window allows past", historyOnlyJID, "past", true},
		{"history window denies ambiguous grant second", historyOnlyJID, "boundary", false},
		{"history window denies future", historyOnlyJID, "future", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.CanMCPReadMessage(tc.id, tc.chatJID)
			if err != nil {
				t.Fatalf("check read permission: %v", err)
			}
			if got != tc.want {
				t.Fatalf("CanMCPReadMessage(%q, %q) = %v, want %v", tc.id, tc.chatJID, got, tc.want)
			}
		})
	}

	chats, err := store.ListMCPAccessChats()
	if err != nil {
		t.Fatalf("list independent windows: %v", err)
	}
	newOnly := findMCPAdminChat(t, chats, newOnlyJID)
	if !newOnly.ReadNew || newOnly.ReadHistory || newOnly.ReadNewSinceUnix == nil ||
		*newOnly.ReadNewSinceUnix != now.Unix()+1 || newOnly.ReadHistoryThroughUnix != nil {
		t.Fatalf("unexpected new-only permission state: %#v", newOnly)
	}
	historyOnly := findMCPAdminChat(t, chats, historyOnlyJID)
	if historyOnly.ReadNew || !historyOnly.ReadHistory || historyOnly.ReadNewSinceUnix != nil ||
		historyOnly.ReadHistoryThroughUnix == nil || *historyOnly.ReadHistoryThroughUnix != now.Unix()-1 {
		t.Fatalf("unexpected history-only permission state: %#v", historyOnly)
	}
}

func TestMCPAccessRevokeAndReenableCreatesGap(t *testing.T) {
	store := newMCPAccessTestStore(t)
	chatJID := "15551234567@s.whatsapp.net"
	firstGrant := time.Unix(1_730_000_000, 0).UTC()
	secondGrant := firstGrant.Add(2 * time.Hour)
	seedMCPAccessChat(t, store, chatJID, "Alice", nil)

	if err := store.UpdateMCPChatPermissions(
		[]MCPChatPermissionUpdate{{ChatJID: chatJID, ReadNew: true}},
		firstGrant,
	); err != nil {
		t.Fatalf("grant new-message access: %v", err)
	}
	seedMCPAccessMessage(t, store, "first-window", chatJID, "visible first", firstGrant.Add(time.Minute))

	// A true -> true save must preserve the original endpoint.
	if err := store.UpdateMCPChatPermissions(
		[]MCPChatPermissionUpdate{{
			ChatJID:          chatJID,
			ReadNew:          true,
			CanSend:          true,
			ExpectedRevision: 1,
		}},
		firstGrant.Add(time.Hour),
	); err != nil {
		t.Fatalf("save unchanged new-message access: %v", err)
	}
	chats, err := store.ListMCPAccessChats()
	if err != nil {
		t.Fatalf("list permissions after unchanged save: %v", err)
	}
	chat := findMCPAdminChat(t, chats, chatJID)
	if chat.ReadNewSinceUnix == nil || *chat.ReadNewSinceUnix != firstGrant.Unix()+1 {
		t.Fatalf("true -> true must preserve endpoint, got %#v", chat.ReadNewSinceUnix)
	}

	if err := store.UpdateMCPChatPermissions(
		[]MCPChatPermissionUpdate{{
			ChatJID:          chatJID,
			ReadNew:          false,
			CanSend:          true,
			ExpectedRevision: 2,
		}},
		firstGrant.Add(90*time.Minute),
	); err != nil {
		t.Fatalf("revoke new-message access: %v", err)
	}
	canRead, err := store.CanMCPReadMessage("first-window", chatJID)
	if err != nil {
		t.Fatalf("check revoked message: %v", err)
	}
	if canRead {
		t.Fatal("revoking the only read window must immediately deny old messages")
	}

	if err := store.UpdateMCPChatPermissions(
		[]MCPChatPermissionUpdate{{
			ChatJID:          chatJID,
			ReadNew:          true,
			CanSend:          true,
			ExpectedRevision: 3,
		}},
		secondGrant,
	); err != nil {
		t.Fatalf("re-enable new-message access: %v", err)
	}
	seedMCPAccessMessage(t, store, "gap", chatJID, "must stay hidden", secondGrant.Add(-time.Minute))
	seedMCPAccessMessage(t, store, "second-window", chatJID, "visible again", secondGrant.Add(time.Minute))

	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"first-window", false},
		{"gap", false},
		{"second-window", true},
	} {
		got, err := store.CanMCPReadMessage(tc.id, chatJID)
		if err != nil {
			t.Fatalf("check message %s after re-enable: %v", tc.id, err)
		}
		if got != tc.want {
			t.Fatalf("message %s readable=%v, want %v", tc.id, got, tc.want)
		}
	}
}

func TestMCPAccessRejectsStalePermissionRevision(t *testing.T) {
	store := newMCPAccessTestStore(t)
	chatJID := "15551234567@s.whatsapp.net"
	seedMCPAccessChat(t, store, chatJID, "Alice", nil)

	if err := store.UpdateMCPChatPermissions(
		[]MCPChatPermissionUpdate{{ChatJID: chatJID, ReadNew: true}},
		time.Unix(1_735_000_000, 0).UTC(),
	); err != nil {
		t.Fatalf("store initial permission: %v", err)
	}
	err := store.UpdateMCPChatPermissions(
		[]MCPChatPermissionUpdate{{
			ChatJID:          chatJID,
			ReadNew:          false,
			CanSend:          true,
			ExpectedRevision: 0,
		}},
		time.Unix(1_735_000_100, 0).UTC(),
	)
	if !errors.Is(err, ErrMCPPermissionConflict) {
		t.Fatalf("stale update error=%v, want ErrMCPPermissionConflict", err)
	}

	chats, listErr := store.ListMCPAccessChats()
	if listErr != nil {
		t.Fatalf("list permissions after conflict: %v", listErr)
	}
	chat := findMCPAdminChat(t, chats, chatJID)
	if !chat.ReadNew || chat.CanSend || chat.PermissionsRevision != 1 {
		t.Fatalf("stale update changed permissions: %#v", chat)
	}
}

func TestMCPAccessBatchIsAtomicForUnknownChat(t *testing.T) {
	store := newMCPAccessTestStore(t)
	knownJID := "15551234567@s.whatsapp.net"
	seedMCPAccessChat(t, store, knownJID, "Alice", nil)

	err := store.UpdateMCPChatPermissions([]MCPChatPermissionUpdate{
		{ChatJID: knownJID, ReadNew: true, ReadHistory: true, CanSend: true},
		{ChatJID: "15559999999@s.whatsapp.net", CanSend: true},
	}, time.Unix(1_740_000_000, 0).UTC())
	if !errors.Is(err, ErrMCPChatNotFound) {
		t.Fatalf("expected ErrMCPChatNotFound, got %v", err)
	}

	var rows int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM mcp_chat_permissions").Scan(&rows); err != nil {
		t.Fatalf("count permission rows after failed batch: %v", err)
	}
	if rows != 0 {
		t.Fatalf("failed batch must roll back all permission rows, got %d", rows)
	}
	canSend, err := store.CanMCPSend(knownJID)
	if err != nil {
		t.Fatalf("check known chat after rollback: %v", err)
	}
	if canSend {
		t.Fatal("known chat must remain denied after another batch JID fails")
	}
}

func TestMCPAccessUsesExactJIDs(t *testing.T) {
	store := newMCPAccessTestStore(t)
	phoneJID := "15551234567@s.whatsapp.net"
	lidJID := "987654321012345@lid"
	seedMCPAccessChat(t, store, phoneJID, "Alice phone", nil)
	seedMCPAccessChat(t, store, lidJID, "Alice LID", nil)

	if err := store.UpdateMCPChatPermissions(
		[]MCPChatPermissionUpdate{{ChatJID: phoneJID, CanSend: true}},
		time.Unix(1_750_000_000, 0).UTC(),
	); err != nil {
		t.Fatalf("grant exact phone JID: %v", err)
	}

	phoneAllowed, err := store.CanMCPSend(phoneJID)
	if err != nil {
		t.Fatalf("check phone JID: %v", err)
	}
	lidAllowed, err := store.CanMCPSend(lidJID)
	if err != nil {
		t.Fatalf("check LID JID: %v", err)
	}
	if !phoneAllowed || lidAllowed {
		t.Fatalf("permissions must not cross JID aliases: phone=%v lid=%v", phoneAllowed, lidAllowed)
	}
}

func TestMCPAdminChatListDoesNotExposeMessageContent(t *testing.T) {
	store := newMCPAccessTestStore(t)
	directJID := "15551234567@s.whatsapp.net"
	groupJID := "120363012345678901@g.us"
	lastMessageTime := time.Unix(1_760_000_000, 0).UTC()
	seedMCPAccessChat(t, store, directJID, "Alice", &lastMessageTime)
	seedMCPAccessChat(t, store, groupJID, "Family", nil)
	seedMCPAccessMessage(t, store, "secret-message", directJID, "TOP-SECRET-CONTENT", lastMessageTime)

	if err := store.UpdateMCPChatPermissions(
		[]MCPChatPermissionUpdate{{ChatJID: directJID, CanSend: true}},
		lastMessageTime.Add(time.Minute),
	); err != nil {
		t.Fatalf("configure direct chat: %v", err)
	}

	chats, err := store.ListMCPAccessChats()
	if err != nil {
		t.Fatalf("list admin chats: %v", err)
	}
	if len(chats) != 2 {
		t.Fatalf("expected two chats, got %d", len(chats))
	}
	if findMCPAdminChat(t, chats, directJID).IsGroup {
		t.Fatal("direct chat must not be marked as a group")
	}
	if !findMCPAdminChat(t, chats, groupJID).IsGroup {
		t.Fatal("group JID must be marked as a group")
	}

	encoded, err := json.Marshal(chats)
	if err != nil {
		t.Fatalf("marshal admin chat list: %v", err)
	}
	if strings.Contains(string(encoded), "TOP-SECRET-CONTENT") {
		t.Fatalf("admin chat JSON leaked message content: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"last_message_time"`) {
		t.Fatalf("admin chat JSON should retain non-content activity metadata: %s", encoded)
	}
}

func TestMCPTimestampWindowHelper(t *testing.T) {
	at := func(value int64) sql.NullInt64 {
		return sql.NullInt64{Int64: value, Valid: true}
	}
	none := sql.NullInt64{}

	cases := []struct {
		name        string
		messageUnix int64
		readNew     sql.NullInt64
		history     sql.NullInt64
		want        bool
	}{
		{"no windows denies", 100, none, none, false},
		{"new includes boundary", 100, at(100), none, true},
		{"new denies earlier", 99, at(100), none, false},
		{"history includes boundary", 100, none, at(100), true},
		{"history denies later", 101, none, at(100), false},
		{"union allows old", 50, at(100), at(90), true},
		{"union preserves gap", 95, at(100), at(90), false},
		{"union allows new", 101, at(100), at(90), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpTimestampWithinReadWindow(tc.messageUnix, tc.readNew, tc.history); got != tc.want {
				t.Fatalf("mcpTimestampWithinReadWindow() = %v, want %v", got, tc.want)
			}
		})
	}
}
