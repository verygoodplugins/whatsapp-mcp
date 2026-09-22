package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waMmsRetry"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/gcmutil"
	"go.mau.fi/whatsmeow/util/hkdfutil"
	"google.golang.org/protobuf/proto"
)

func TestExpiredMediaRefresh(t *testing.T) {
	for _, expired := range []error{whatsmeow.ErrMediaDownloadFailedWith403, whatsmeow.ErrMediaDownloadFailedWith404, whatsmeow.ErrMediaDownloadFailedWith410} {
		t.Run(expired.Error(), func(t *testing.T) {
			media := &MediaDownloader{URL: "https://mmg.whatsapp.net/old?oe=expired", DirectPath: "/old?oe=expired"}
			calls, refreshes, saved := 0, 0, ""
			data, err := downloadWithRefresh(context.Background(), media, func(_ context.Context, m *MediaDownloader) ([]byte, error) {
				calls++
				if calls == 1 {
					return nil, fmt.Errorf("wrapped: %w", expired)
				}
				if m.DirectPath != "/fresh?oe=valid" {
					t.Fatalf("retry used stale path")
				}
				return []byte("PDF bytes"), nil
			}, func(context.Context) (string, error) {
				refreshes++
				return "/fresh?oe=valid", nil
			}, func(path string) error { saved = path; return nil })
			if err != nil || string(data) != "PDF bytes" || calls != 2 || refreshes != 1 || saved != "/fresh?oe=valid" {
				t.Fatalf("data=%q err=%v downloads=%d refreshes=%d saved=%q", data, err, calls, refreshes, saved)
			}
		})
	}
}

func TestMediaRefreshPhoneResponse(t *testing.T) {
	manager := &mediaRefreshManager{}
	info := &types.MessageInfo{MessageSource: types.MessageSource{Chat: types.NewJID("123", types.GroupServer), Sender: types.NewJID("456", types.DefaultUserServer), IsGroup: true}, ID: "test-message"}
	key := []byte("01234567890123456789012345678901")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	path, err := manager.request(ctx, info, key, func(_ context.Context, got *types.MessageInfo, gotKey []byte) error {
		if got.Sender != info.Sender || !got.IsGroup || string(gotKey) != string(key) {
			t.Fatal("lost group sender or media key")
		}
		// Unrelated chats/direction and duplicate notifications must not consume the waiter.
		manager.handleEvent(&events.MediaRetry{MessageID: info.ID, ChatID: types.NewJID("other", types.GroupServer), Error: &events.MediaRetryError{Code: 2}})
		manager.handleEvent(&events.MediaRetry{MessageID: info.ID, ChatID: info.Chat, FromMe: true, Error: &events.MediaRetryError{Code: 2}})
		plain, marshalErr := proto.Marshal(&waMmsRetry.MediaRetryNotification{Result: waMmsRetry.MediaRetryNotification_SUCCESS.Enum(), DirectPath: proto.String("/fresh?token=example")})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		iv := make([]byte, 12)
		ciphertext, encryptErr := gcmutil.Encrypt(hkdfutil.SHA256(key, nil, []byte("WhatsApp Media Retry Notification"), 32), iv, plain, []byte(info.ID))
		if encryptErr != nil {
			t.Fatal(encryptErr)
		}
		evt := &events.MediaRetry{MessageID: info.ID, ChatID: info.Chat, IV: iv, Ciphertext: ciphertext}
		manager.handleEvent(evt)
		manager.handleEvent(evt)
		return nil
	})
	if err != nil || path != "/fresh?token=example" {
		t.Fatalf("path=%q err=%v", path, err)
	}
	if len(manager.waiters) != 0 {
		t.Fatal("waiter leaked")
	}
}

func TestMediaRefreshUnavailableAndTimeout(t *testing.T) {
	for _, unavailable := range []bool{true, false} {
		manager := &mediaRefreshManager{}
		info := &types.MessageInfo{MessageSource: types.MessageSource{Chat: types.NewJID("123", types.DefaultUserServer)}, ID: "test"}
		ctx, cancel := context.WithCancel(context.Background())
		_, err := manager.request(ctx, info, nil, func(context.Context, *types.MessageInfo, []byte) error {
			if unavailable {
				manager.handleEvent(&events.MediaRetry{MessageID: info.ID, ChatID: info.Chat, Error: &events.MediaRetryError{Code: 2}})
			} else {
				cancel()
			}
			return nil
		})
		cancel()
		want := context.Canceled
		if unavailable {
			want = whatsmeow.ErrMediaNotAvailableOnPhone
		}
		if !errors.Is(err, want) {
			t.Fatalf("got %v want %v", err, want)
		}
		if len(manager.waiters) != 0 {
			t.Fatal("waiter leaked")
		}
	}
}

func TestMediaRefreshRetryBoundAndInvalidPath(t *testing.T) {
	for _, path := range []string{"/fresh?token=example", "", "https://example.com", "//example.com", "/bad%zz", "/bad\npath", "/bad#fragment", "/\\example.com"} {
		calls, saves := 0, 0
		_, err := downloadWithRefresh(context.Background(), &MediaDownloader{}, func(context.Context, *MediaDownloader) ([]byte, error) {
			calls++
			return nil, whatsmeow.ErrMediaDownloadFailedWith403
		}, func(context.Context) (string, error) { return path, nil }, func(string) error { saves++; return nil })
		if err == nil {
			t.Fatal("expected error")
		}
		if path == "/fresh?token=example" {
			if calls != 2 || saves != 1 {
				t.Fatalf("downloads=%d saves=%d", calls, saves)
			}
		} else if calls != 1 || saves != 0 {
			t.Fatal("invalid path used")
		}
	}
}

func TestOtherDownloadErrorsDoNotRefresh(t *testing.T) {
	want := errors.New("network error")
	_, err := downloadWithRefresh(context.Background(), &MediaDownloader{}, func(context.Context, *MediaDownloader) ([]byte, error) { return nil, want }, func(context.Context) (string, error) { t.Fatal("unexpected refresh"); return "", nil }, func(string) error { t.Fatal("unexpected write"); return nil })
	if !errors.Is(err, want) {
		t.Fatalf("lost original error: %v", err)
	}
}

func TestMediaRefreshMatchesPhoneAndLIDAliases(t *testing.T) {
	pn := types.NewJID("123", types.DefaultUserServer)
	lid := types.NewJID("456", types.HiddenUserServer)
	manager := &mediaRefreshManager{normalizeChat: func(j types.JID) types.JID {
		if j == lid {
			return pn
		}
		return j
	}}
	info := &types.MessageInfo{MessageSource: types.MessageSource{Chat: pn}, ID: "alias-test"}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := manager.request(ctx, info, nil, func(context.Context, *types.MessageInfo, []byte) error {
		manager.handleEvent(&events.MediaRetry{MessageID: info.ID, ChatID: lid, Error: &events.MediaRetryError{Code: 2}})
		return nil
	})
	if !errors.Is(err, whatsmeow.ErrMediaNotAvailableOnPhone) {
		t.Fatalf("alias reply was not matched: %v", err)
	}
	if len(manager.waiters) != 0 {
		t.Fatal("waiter leaked")
	}
}

func encryptedMediaResponse(t *testing.T, info *types.MessageInfo, key []byte, result waMmsRetry.MediaRetryNotification_ResultType) *events.MediaRetry {
	t.Helper()
	plain, err := proto.Marshal(&waMmsRetry.MediaRetryNotification{Result: result.Enum(), DirectPath: proto.String("/fresh?oe=valid")})
	if err != nil {
		t.Fatal(err)
	}
	iv := make([]byte, 12)
	ciphertext, err := gcmutil.Encrypt(hkdfutil.SHA256(key, nil, []byte("WhatsApp Media Retry Notification"), 32), iv, plain, []byte(info.ID))
	if err != nil {
		t.Fatal(err)
	}
	return &events.MediaRetry{MessageID: info.ID, ChatID: info.Chat, FromMe: info.IsFromMe, IV: iv, Ciphertext: ciphertext}
}

func TestMediaRefreshRejectsInvalidPhoneResponse(t *testing.T) {
	info := &types.MessageInfo{MessageSource: types.MessageSource{Chat: types.NewJID("123", types.DefaultUserServer)}, ID: "test"}
	key := []byte("01234567890123456789012345678901")
	for _, kind := range []string{"bad-iv", "tampered", "wrong-key", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			manager := &mediaRefreshManager{}
			evt := encryptedMediaResponse(t, info, key, waMmsRetry.MediaRetryNotification_SUCCESS)
			switch kind {
			case "bad-iv":
				evt.IV = []byte{1}
			case "tampered":
				evt.Ciphertext[0] ^= 1
			case "wrong-key":
				evt = encryptedMediaResponse(t, info, []byte("another key"), waMmsRetry.MediaRetryNotification_SUCCESS)
			case "unavailable":
				evt = encryptedMediaResponse(t, info, key, waMmsRetry.MediaRetryNotification_NOT_FOUND)
			}
			path, err := manager.request(context.Background(), info, key, func(context.Context, *types.MessageInfo, []byte) error {
				manager.handleEvent(evt)
				return nil
			})
			if err == nil || path != "" {
				t.Fatal("accepted an invalid phone response")
			}
			if len(manager.waiters) != 0 {
				t.Fatal("waiter leaked")
			}
		})
	}
}

func TestMediaRefreshConcurrentWaiters(t *testing.T) {
	manager := &mediaRefreshManager{}
	info := &types.MessageInfo{MessageSource: types.MessageSource{Chat: types.NewJID("123", types.DefaultUserServer)}, ID: "shared"}
	key := []byte("01234567890123456789012345678901")
	evt := encryptedMediaResponse(t, info, key, waMmsRetry.MediaRetryNotification_SUCCESS)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	canceled, cancelOne := context.WithCancel(ctx)
	defer cancelOne()
	const callers = 8
	ready := make(chan struct{}, callers)
	canceledDone := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			requestCtx := ctx
			if i == 0 {
				requestCtx = canceled
				defer close(canceledDone)
			}
			path, err := manager.request(requestCtx, info, key, func(context.Context, *types.MessageInfo, []byte) error {
				ready <- struct{}{}
				return nil
			})
			if i == 0 {
				if !errors.Is(err, context.Canceled) {
					t.Errorf("canceled request returned %v", err)
				}
			} else if err != nil || path != "/fresh?oe=valid" {
				t.Errorf("shared response was not delivered: %v", err)
			}
		})
	}
	for range callers {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("requests did not subscribe")
		}
	}
	cancelOne()
	<-canceledDone
	manager.handleEvent(evt)
	manager.handleEvent(evt)
	wg.Wait()
	if len(manager.waiters) != 0 {
		t.Fatal("waiter leaked")
	}
	manager.handleEvent(evt) // Late replies after cleanup are harmless.
}

func TestMediaRefreshSendFailureAndDeadline(t *testing.T) {
	info := &types.MessageInfo{MessageSource: types.MessageSource{Chat: types.NewJID("123", types.DefaultUserServer)}, ID: "test"}
	for _, sendFailure := range []bool{false, true} {
		manager := &mediaRefreshManager{}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		want := context.DeadlineExceeded
		if sendFailure {
			want = errors.New("send failed")
		}
		_, err := manager.request(ctx, info, nil, func(context.Context, *types.MessageInfo, []byte) error {
			if sendFailure {
				return want
			}
			return nil
		})
		cancel()
		if !errors.Is(err, want) || len(manager.waiters) != 0 {
			t.Fatalf("request error=%v waiters=%d", err, len(manager.waiters))
		}
	}
}

func TestMediaRefreshSharedDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	_, err := downloadWithRefresh(ctx, &MediaDownloader{}, func(got context.Context, _ *MediaDownloader) ([]byte, error) {
		if got != ctx {
			t.Fatal("download replaced the request deadline")
		}
		calls++
		return nil, whatsmeow.ErrMediaDownloadFailedWith410
	}, func(got context.Context) (string, error) {
		if got != ctx {
			t.Fatal("refresh replaced the request deadline")
		}
		cancel()
		return "/fresh?oe=valid", nil
	}, func(string) error { t.Fatal("persisted after cancellation"); return nil })
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("error=%v downloads=%d", err, calls)
	}
}

func TestMediaRefreshPersistFailure(t *testing.T) {
	want := errors.New("write failed")
	calls := 0
	_, err := downloadWithRefresh(context.Background(), &MediaDownloader{}, func(context.Context, *MediaDownloader) ([]byte, error) {
		calls++
		return nil, whatsmeow.ErrMediaDownloadFailedWith404
	}, func(context.Context) (string, error) { return "/fresh?oe=valid", nil }, func(string) error { return want })
	if !errors.Is(err, want) || calls != 1 {
		t.Fatalf("error=%v downloads=%d", err, calls)
	}
}
