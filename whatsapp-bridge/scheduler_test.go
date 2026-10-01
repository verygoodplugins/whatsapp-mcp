package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeSender struct {
	sent    []string
	failFor map[string]int
}

func (f *fakeSender) send(_ string, message string) (bool, string) {
	if f.failFor[message] > 0 {
		f.failFor[message]--
		return false, "boom"
	}
	f.sent = append(f.sent, message)
	return true, "ok"
}

type schedulerFixture struct {
	scheduler *Scheduler
	sender    *fakeSender
	clock     time.Time
	connected bool
}

func newSchedulerFixture(t *testing.T) *schedulerFixture {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := ensureScheduledMessagesTable(db); err != nil {
		t.Fatalf("create table: %v", err)
	}

	f := &schedulerFixture{
		sender:    &fakeSender{failFor: map[string]int{}},
		clock:     time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		connected: true,
	}
	f.scheduler = &Scheduler{
		db:          db,
		send:        f.sender.send,
		isConnected: func() bool { return f.connected },
		chatJIDFor:  func(recipient string) (string, error) { return recipient + "@s.whatsapp.net", nil },
		now:         func() time.Time { return f.clock },
	}
	return f
}

func (f *schedulerFixture) schedule(t *testing.T, recipient, message string, in time.Duration) ScheduledMessage {
	t.Helper()
	msg, err := f.scheduler.Schedule(recipient, message, f.clock.Add(in))
	if err != nil {
		t.Fatalf("schedule %q: %v", message, err)
	}
	return msg
}

func (f *schedulerFixture) advance(d time.Duration) {
	f.clock = f.clock.Add(d)
	f.scheduler.ProcessDue()
}

func (f *schedulerFixture) status(t *testing.T, id int64) ScheduleStatus {
	t.Helper()
	msg, err := f.scheduler.get(id)
	if err != nil {
		t.Fatalf("get %d: %v", id, err)
	}
	return msg.Status
}

func assertSent(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("sent %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sent %v, want %v", got, want)
		}
	}
}

func TestSchedulerSendsOnlyDueMessages(t *testing.T) {
	f := newSchedulerFixture(t)
	f.schedule(t, "1", "later", time.Hour)

	f.advance(59 * time.Minute)
	assertSent(t, f.sender.sent)

	f.advance(time.Minute)
	assertSent(t, f.sender.sent, "later")
}

func TestSchedulerKeepsChatOrderBySendTimeThenCreation(t *testing.T) {
	f := newSchedulerFixture(t)
	f.schedule(t, "1", "third", 2*time.Minute)
	f.schedule(t, "1", "first", time.Minute)
	f.schedule(t, "1", "second", time.Minute)

	f.advance(time.Hour)
	assertSent(t, f.sender.sent, "first", "second", "third")
}

func TestSchedulerRetryHoldsBackLaterMessagesInSameChatOnly(t *testing.T) {
	f := newSchedulerFixture(t)
	head := f.schedule(t, "1", "flaky", 0)
	f.schedule(t, "1", "after flaky", 0)
	f.schedule(t, "2", "other chat", 0)
	f.sender.failFor["flaky"] = 1

	f.advance(0)
	assertSent(t, f.sender.sent, "other chat")
	if got := f.status(t, head.ID); got != StatusPending {
		t.Fatalf("head status = %s, want pending retry", got)
	}

	f.advance(retryDelay(1))
	assertSent(t, f.sender.sent, "other chat", "flaky", "after flaky")
}

func TestSchedulerFailedHeadBlocksQueueUntilCancelled(t *testing.T) {
	f := newSchedulerFixture(t)
	head := f.schedule(t, "1", "broken", 0)
	f.schedule(t, "1", "blocked", 0)
	f.sender.failFor["broken"] = maxSendAttempts

	for range maxSendAttempts {
		f.advance(time.Hour)
	}
	if got := f.status(t, head.ID); got != StatusFailed {
		t.Fatalf("head status = %s, want failed", got)
	}
	assertSent(t, f.sender.sent)

	if err := f.scheduler.Cancel(head.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	f.advance(0)
	assertSent(t, f.sender.sent, "blocked")
}

func TestSchedulerDoesNotSpendAttemptsWhileDisconnected(t *testing.T) {
	f := newSchedulerFixture(t)
	msg := f.schedule(t, "1", "hello", 0)
	f.connected = false

	f.advance(time.Hour)
	got, _ := f.scheduler.get(msg.ID)
	if got.Status != StatusPending || got.Attempts != 0 {
		t.Fatalf("got status=%s attempts=%d, want untouched pending", got.Status, got.Attempts)
	}

	f.connected = true
	f.advance(0)
	assertSent(t, f.sender.sent, "hello")
}

func TestSchedulerSendingHeadHoldsBackLaterMessagesInSameChat(t *testing.T) {
	f := newSchedulerFixture(t)
	head := f.schedule(t, "1", "in flight", 0)
	f.schedule(t, "1", "must wait", 0)
	if _, err := f.scheduler.db.Exec(`UPDATE scheduled_messages SET status = ? WHERE id = ?`, StatusSending, head.ID); err != nil {
		t.Fatal(err)
	}

	f.advance(time.Hour)
	assertSent(t, f.sender.sent)
}

func TestSchedulerStopsPassWhenClaimFails(t *testing.T) {
	f := newSchedulerFixture(t)
	f.schedule(t, "1", "unclaimable", 0)
	_, err := f.scheduler.db.Exec(`
		CREATE TRIGGER block_claim BEFORE UPDATE OF status ON scheduled_messages
		WHEN NEW.status = 'sending' BEGIN SELECT RAISE(ABORT, 'claim blocked'); END`)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		f.scheduler.ProcessDue()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ProcessDue kept retrying a claim that fails")
	}
	assertSent(t, f.sender.sent)
}

func TestSchedulerRecoverInterruptedBlocksInsteadOfResending(t *testing.T) {
	f := newSchedulerFixture(t)
	msg := f.schedule(t, "1", "in flight", 0)
	if _, err := f.scheduler.db.Exec(`UPDATE scheduled_messages SET status = ? WHERE id = ?`, StatusSending, msg.ID); err != nil {
		t.Fatal(err)
	}

	if err := f.scheduler.RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Hour)
	assertSent(t, f.sender.sent)
	if got := f.status(t, msg.ID); got != StatusFailed {
		t.Fatalf("status = %s, want failed", got)
	}
}

func TestSchedulerCancelRules(t *testing.T) {
	f := newSchedulerFixture(t)
	msg := f.schedule(t, "1", "hello", 0)
	f.advance(0)

	if err := f.scheduler.Cancel(msg.ID); !errors.Is(err, ErrScheduleNotCancellable) {
		t.Fatalf("cancel sent message: err = %v, want ErrScheduleNotCancellable", err)
	}
	if err := f.scheduler.Cancel(999); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("cancel unknown id: err = %v, want ErrScheduleNotFound", err)
	}
}

func TestScheduleEndpoints(t *testing.T) {
	f := newSchedulerFixture(t)
	mux := http.NewServeMux()
	noAuth := func(h http.HandlerFunc) http.HandlerFunc { return h }
	registerScheduleEndpoints(mux, noAuth, f.scheduler)

	post := func(path, body string) (int, ScheduleResponse) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		var resp ScheduleResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("%s: decode response: %v", path, err)
		}
		return rec.Code, resp
	}

	code, resp := post("/api/schedule", `{"recipient":"1","message":"hi","send_at":"2026-10-01T15:00:00-03:00"}`)
	if code != http.StatusOK || !resp.Success || resp.Scheduled == nil {
		t.Fatalf("schedule: code=%d resp=%+v", code, resp)
	}
	if got := resp.Scheduled.SendAt.UTC(); !got.Equal(time.Date(2026, 10, 1, 18, 0, 0, 0, time.UTC)) {
		t.Fatalf("send_at stored as %s, offset was not honoured", got)
	}

	if code, _ := post("/api/schedule", `{"recipient":"1","message":"hi","send_at":"2026-10-01T15:00"}`); code != http.StatusBadRequest {
		t.Fatalf("send_at without offset: code=%d, want 400", code)
	}
	if code, _ := post("/api/schedule/cancel", `{"id":999}`); code != http.StatusNotFound {
		t.Fatalf("cancel unknown: code=%d, want 404", code)
	}
	if code, resp := post("/api/schedule/cancel", fmt.Sprintf(`{"id":%d}`, resp.Scheduled.ID)); code != http.StatusOK || !resp.Success {
		t.Fatalf("cancel: code=%d resp=%+v", code, resp)
	}
	if code, _ := post("/api/schedule/cancel", fmt.Sprintf(`{"id":%d}`, resp.Scheduled.ID)); code != http.StatusConflict {
		t.Fatalf("cancel twice: code=%d, want 409", code)
	}
}

func TestSchedulerRejectsInvalidInput(t *testing.T) {
	f := newSchedulerFixture(t)
	cases := map[string]struct {
		recipient, message string
		sendAt             time.Time
	}{
		"empty recipient": {"", "hi", f.clock},
		"empty message":   {"1", "  ", f.clock},
		"past send_at":    {"1", "hi", f.clock.Add(-2 * pastSendAtTolerance)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := f.scheduler.Schedule(tc.recipient, tc.message, tc.sendAt); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
