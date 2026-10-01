package main

// Scheduled messages.
//
// Each chat has its own FIFO queue ordered by (send_at, id). Only the head of a
// chat's queue is ever sent, so a message can never overtake an earlier one in
// the same chat, even while the earlier one is being retried. A head that
// exhausts its retries is marked failed and blocks the rest of its queue until
// it is cancelled: silently skipping it would deliver later messages out of
// order, which is exactly what the queue exists to prevent.
//
// The scheduler lives in the bridge rather than the MCP server because the
// bridge is the long-running process; MCP servers come and go with clients.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

type ScheduleStatus string

const (
	StatusPending   ScheduleStatus = "pending"
	StatusSending   ScheduleStatus = "sending"
	StatusSent      ScheduleStatus = "sent"
	StatusFailed    ScheduleStatus = "failed"
	StatusCancelled ScheduleStatus = "cancelled"
)

const (
	maxSendAttempts       = 5
	firstRetryDelay       = 30 * time.Second
	schedulerPollInterval = 2 * time.Second
	// Clock skew between the MCP client and the bridge shouldn't turn "now"
	// into a rejected past time.
	pastSendAtTolerance = time.Minute
)

var (
	ErrScheduleNotFound       = errors.New("scheduled message not found")
	ErrScheduleNotCancellable = errors.New("scheduled message is already being sent, sent or cancelled")
)

type ScheduledMessage struct {
	ID            int64          `json:"id"`
	ChatJID       string         `json:"chat_jid"`
	Recipient     string         `json:"recipient"`
	Message       string         `json:"message"`
	SendAt        time.Time      `json:"send_at"`
	Status        ScheduleStatus `json:"status"`
	Attempts      int            `json:"attempts"`
	NextAttemptAt *time.Time     `json:"next_attempt_at,omitempty"`
	LastError     string         `json:"last_error,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	SentAt        *time.Time     `json:"sent_at,omitempty"`
}

type Scheduler struct {
	db          *sql.DB
	send        func(recipient, message string) (bool, string)
	isConnected func() bool
	chatJIDFor  func(recipient string) (string, error)
	now         func() time.Time
}

func ensureScheduledMessagesTable(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS scheduled_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_jid TEXT NOT NULL,
			recipient TEXT NOT NULL,
			message TEXT NOT NULL,
			send_at INTEGER NOT NULL,
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			next_attempt_at INTEGER,
			last_error TEXT,
			created_at INTEGER NOT NULL,
			sent_at INTEGER
		);
		CREATE INDEX IF NOT EXISTS idx_scheduled_messages_queue
			ON scheduled_messages(chat_jid, status, send_at, id);
	`)
	return err
}

// chatJIDForRecipient maps every form of a recipient (bare phone, phone JID,
// LID JID) to one chat JID, so a contact never ends up with two queues.
func chatJIDForRecipient(client *whatsmeow.Client, recipient string) (string, error) {
	if !strings.Contains(recipient, "@") {
		return types.NewJID(recipient, types.DefaultUserServer).String(), nil
	}
	jid, err := types.ParseJID(recipient)
	if err != nil {
		return "", fmt.Errorf("invalid recipient JID: %w", err)
	}
	if jid.Server == types.HiddenUserServer && client.Store != nil && client.Store.LIDs != nil {
		if pn, err := client.Store.LIDs.GetPNForLID(context.Background(), jid); err == nil && !pn.IsEmpty() {
			return pn.ToNonAD().String(), nil
		}
	}
	return jid.ToNonAD().String(), nil
}

func (s *Scheduler) Schedule(recipient, message string, sendAt time.Time) (ScheduledMessage, error) {
	if strings.TrimSpace(recipient) == "" {
		return ScheduledMessage{}, errors.New("recipient is required")
	}
	if strings.TrimSpace(message) == "" {
		return ScheduledMessage{}, errors.New("message is required")
	}
	now := s.now()
	if sendAt.Before(now.Add(-pastSendAtTolerance)) {
		return ScheduledMessage{}, fmt.Errorf("send_at %s is in the past", sendAt.Format(time.RFC3339))
	}
	chatJID, err := s.chatJIDFor(recipient)
	if err != nil {
		return ScheduledMessage{}, err
	}

	res, err := s.db.Exec(
		`INSERT INTO scheduled_messages (chat_jid, recipient, message, send_at, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		chatJID, recipient, message, sendAt.Unix(), StatusPending, now.Unix(),
	)
	if err != nil {
		return ScheduledMessage{}, fmt.Errorf("failed to store scheduled message: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ScheduledMessage{}, fmt.Errorf("failed to read scheduled message id: %w", err)
	}
	return s.get(id)
}

func (s *Scheduler) Cancel(id int64) error {
	res, err := s.db.Exec(
		`UPDATE scheduled_messages SET status = ?, next_attempt_at = NULL
		  WHERE id = ? AND status IN (?, ?)`,
		StatusCancelled, id, StatusPending, StatusFailed,
	)
	if err != nil {
		return fmt.Errorf("failed to cancel scheduled message: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	if _, err := s.get(id); errors.Is(err, sql.ErrNoRows) {
		return ErrScheduleNotFound
	}
	return ErrScheduleNotCancellable
}

// RecoverInterrupted fails messages left mid-send by a crash. Whether they
// reached WhatsApp is unknown, so they block their queue instead of being
// resent and possibly duplicated.
func (s *Scheduler) RecoverInterrupted() error {
	_, err := s.db.Exec(
		`UPDATE scheduled_messages SET status = ?, last_error = ? WHERE status = ?`,
		StatusFailed, "bridge stopped while sending; delivery unknown, cancel to unblock the queue", StatusSending,
	)
	return err
}

func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(schedulerPollInterval)
	defer ticker.Stop()
	for {
		s.ProcessDue()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ProcessDue sends every queue head that is due, repeating until no head is,
// so several messages due at once in a chat all go out in a single pass. A
// pass that claims nothing stops, so a failing claim cannot spin the loop.
func (s *Scheduler) ProcessDue() {
	for s.isConnected() {
		heads, err := s.dueHeads()
		if err != nil {
			fmt.Printf("Scheduler: failed to load due messages: %v\n", err)
			return
		}
		claimed := false
		for _, head := range heads {
			claimed = s.deliver(head) || claimed
		}
		if !claimed {
			return
		}
	}
}

// dueHeads returns the due, pending head of every chat queue. A head that is
// still sending (or failed) is not returned but keeps holding its queue.
func (s *Scheduler) dueHeads() ([]ScheduledMessage, error) {
	now := s.now().Unix()
	rows, err := s.db.Query(`
		SELECT `+scheduledMessageColumns+`
		  FROM scheduled_messages s
		 WHERE s.status = ?
		   AND s.send_at <= ?
		   AND COALESCE(s.next_attempt_at, 0) <= ?
		   AND s.id = (
		       SELECT h.id FROM scheduled_messages h
		        WHERE h.chat_jid = s.chat_jid AND h.status IN (?, ?, ?)
		        ORDER BY h.send_at, h.id
		        LIMIT 1)
		 ORDER BY s.send_at, s.id`,
		StatusPending, now, now, StatusPending, StatusSending, StatusFailed,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanScheduledMessages(rows)
}

// deliver claims msg, sends it and records the outcome. It reports whether
// the claim succeeded; a message cancelled since it was loaded is skipped.
func (s *Scheduler) deliver(msg ScheduledMessage) bool {
	claimed, err := s.db.Exec(
		`UPDATE scheduled_messages SET status = ? WHERE id = ? AND status = ?`,
		StatusSending, msg.ID, StatusPending,
	)
	if err != nil {
		fmt.Printf("Scheduler: failed to claim message %d: %v\n", msg.ID, err)
		return false
	}
	if n, _ := claimed.RowsAffected(); n == 0 {
		return false
	}

	ok, result := s.send(msg.Recipient, msg.Message)
	now := s.now()
	attempts := msg.Attempts + 1

	switch {
	case ok:
		_, err = s.db.Exec(
			`UPDATE scheduled_messages
			    SET status = ?, attempts = ?, sent_at = ?, next_attempt_at = NULL, last_error = NULL
			  WHERE id = ?`,
			StatusSent, attempts, now.Unix(), msg.ID,
		)
	case attempts >= maxSendAttempts:
		_, err = s.db.Exec(
			`UPDATE scheduled_messages SET status = ?, attempts = ?, next_attempt_at = NULL, last_error = ? WHERE id = ?`,
			StatusFailed, attempts, result, msg.ID,
		)
	default:
		_, err = s.db.Exec(
			`UPDATE scheduled_messages SET status = ?, attempts = ?, next_attempt_at = ?, last_error = ? WHERE id = ?`,
			StatusPending, attempts, now.Add(retryDelay(attempts)).Unix(), result, msg.ID,
		)
	}
	if err != nil {
		fmt.Printf("Scheduler: failed to record outcome of message %d: %v\n", msg.ID, err)
	}
	fmt.Printf("Scheduler: message %d to %s sent=%v attempt=%d\n", msg.ID, msg.ChatJID, ok, attempts)
	return true
}

func retryDelay(attempts int) time.Duration {
	return firstRetryDelay << (attempts - 1)
}

const scheduledMessageColumns = `s.id, s.chat_jid, s.recipient, s.message, s.send_at, s.status, s.attempts,
	s.next_attempt_at, s.last_error, s.created_at, s.sent_at`

func (s *Scheduler) get(id int64) (ScheduledMessage, error) {
	rows, err := s.db.Query(`SELECT `+scheduledMessageColumns+` FROM scheduled_messages s WHERE s.id = ?`, id)
	if err != nil {
		return ScheduledMessage{}, err
	}
	defer func() { _ = rows.Close() }()
	msgs, err := scanScheduledMessages(rows)
	if err != nil {
		return ScheduledMessage{}, err
	}
	if len(msgs) == 0 {
		return ScheduledMessage{}, sql.ErrNoRows
	}
	return msgs[0], nil
}

func scanScheduledMessages(rows *sql.Rows) ([]ScheduledMessage, error) {
	var msgs []ScheduledMessage
	for rows.Next() {
		var (
			m                     ScheduledMessage
			sendAt, createdAt     int64
			nextAttemptAt, sentAt sql.NullInt64
			lastError             sql.NullString
		)
		if err := rows.Scan(&m.ID, &m.ChatJID, &m.Recipient, &m.Message, &sendAt, &m.Status, &m.Attempts,
			&nextAttemptAt, &lastError, &createdAt, &sentAt); err != nil {
			return nil, err
		}
		m.SendAt = time.Unix(sendAt, 0)
		m.CreatedAt = time.Unix(createdAt, 0)
		m.NextAttemptAt = optionalTime(nextAttemptAt)
		m.SentAt = optionalTime(sentAt)
		m.LastError = lastError.String
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func optionalTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0)
	return &t
}

type ScheduleRequest struct {
	Recipient string `json:"recipient"`
	Message   string `json:"message"`
	SendAt    string `json:"send_at"`
}

type CancelScheduleRequest struct {
	ID int64 `json:"id"`
}

type ScheduleResponse struct {
	Success   bool              `json:"success"`
	Message   string            `json:"message"`
	Scheduled *ScheduledMessage `json:"scheduled,omitempty"`
}

func registerScheduleEndpoints(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, scheduler *Scheduler) {
	mux.HandleFunc("/api/schedule", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req ScheduleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeScheduleResponse(w, http.StatusBadRequest, ScheduleResponse{Message: "Invalid request format"})
			return
		}
		sendAt, err := time.Parse(time.RFC3339, req.SendAt)
		if err != nil {
			writeScheduleResponse(w, http.StatusBadRequest, ScheduleResponse{
				Message: "send_at must be an RFC3339 timestamp with a UTC offset, e.g. 2026-10-01T18:30:00-03:00",
			})
			return
		}
		scheduled, err := scheduler.Schedule(req.Recipient, req.Message, sendAt)
		if err != nil {
			writeScheduleResponse(w, http.StatusBadRequest, ScheduleResponse{Message: err.Error()})
			return
		}
		writeScheduleResponse(w, http.StatusOK, ScheduleResponse{
			Success:   true,
			Message:   fmt.Sprintf("Scheduled for %s", scheduled.SendAt.Format(time.RFC3339)),
			Scheduled: &scheduled,
		})
	}))

	mux.HandleFunc("/api/schedule/cancel", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req CancelScheduleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
			writeScheduleResponse(w, http.StatusBadRequest, ScheduleResponse{Message: "A positive id is required"})
			return
		}
		switch err := scheduler.Cancel(req.ID); {
		case err == nil:
			writeScheduleResponse(w, http.StatusOK, ScheduleResponse{Success: true, Message: "Cancelled"})
		case errors.Is(err, ErrScheduleNotFound):
			writeScheduleResponse(w, http.StatusNotFound, ScheduleResponse{Message: err.Error()})
		case errors.Is(err, ErrScheduleNotCancellable):
			writeScheduleResponse(w, http.StatusConflict, ScheduleResponse{Message: err.Error()})
		default:
			writeScheduleResponse(w, http.StatusInternalServerError, ScheduleResponse{Message: err.Error()})
		}
	}))
}

func writeScheduleResponse(w http.ResponseWriter, status int, body ScheduleResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func newScheduler(client *whatsmeow.Client, messageStore *MessageStore) *Scheduler {
	return &Scheduler{
		db: messageStore.db,
		send: func(recipient, message string) (bool, string) {
			return sendWhatsAppMessage(client, messageStore, recipient, message, "", "", "", "", nil)
		},
		isConnected: client.IsConnected,
		chatJIDFor: func(recipient string) (string, error) {
			return chatJIDForRecipient(client, recipient)
		},
		now: time.Now,
	}
}
