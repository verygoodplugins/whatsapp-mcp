package main

import (
	"database/sql"
	"testing"
	"time"
)

func TestStoreReceiptKeepsTheFirstOfEachKind(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE receipts (message_id TEXT, chat_jid TEXT, reader TEXT, type TEXT,
		timestamp TIMESTAMP, PRIMARY KEY (message_id, chat_jid, reader, type))`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	store := &MessageStore{db: db}
	chat := "12025550100@s.whatsapp.net"
	// a receipt in another UTC offset (e.g. across a DST change) still compares as an instant
	first := time.Date(2026, 10, 20, 23, 0, 0, 0, time.FixedZone("CEST", 2*3600))
	if err := store.StoreReceipt(chat, "12025550100", "delivered", []string{"m1", "m2"}, first); err != nil {
		t.Fatalf("store delivered: %v", err)
	}
	if err := store.StoreReceipt(chat, "12025550100", "read", []string{"m1"}, first.Add(time.Minute)); err != nil {
		t.Fatalf("store read: %v", err)
	}
	// a repeated receipt keeps the first time
	if err := store.StoreReceipt(chat, "12025550100", "read", []string{"m1"}, first.Add(time.Hour).In(time.FixedZone("CET", 3600))); err != nil {
		t.Fatalf("store read again: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM receipts`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("rows = %d (%v), want 3", n, err)
	}
	var at time.Time
	if err := db.QueryRow(`SELECT timestamp FROM receipts WHERE message_id = 'm1' AND type = 'read'`).Scan(&at); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if !at.Equal(first.Add(time.Minute)) {
		t.Fatalf("read at %v, want the first receipt's time", at)
	}
}
