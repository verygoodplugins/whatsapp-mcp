package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waMmsRetry"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Retry expired CDN links once using a path reissued by the primary phone.
func downloadWithRefresh(ctx context.Context, media *MediaDownloader, download func(context.Context, *MediaDownloader) ([]byte, error), refresh func(context.Context) (string, error), persist func(string) error) ([]byte, error) {
	data, err := download(ctx, media)
	if !errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith403) && !errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) && !errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410) {
		return data, err
	}
	path, err := refresh(ctx)
	if err != nil {
		return nil, fmt.Errorf("refresh expired media: %w", err)
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, errors.New("phone returned an invalid media path")
	}
	// Save the refreshed path even if this particular download is interrupted.
	if err = persist(path); err != nil {
		return nil, fmt.Errorf("save refreshed media path: %w", err)
	}
	media.DirectPath, media.URL = path, ""
	return download(ctx, media)
}

type mediaRefreshKey struct {
	chat, id string
	fromMe   bool
}

// Registered once at startup: registering temporary whatsmeow handlers from a
// message callback would deadlock its event-handler lock.
type mediaRefreshManager struct {
	normalizeChat func(types.JID) types.JID
	mu            sync.Mutex
	waiters       map[mediaRefreshKey]map[chan *events.MediaRetry]struct{}
}

var mediaRefresh = &mediaRefreshManager{waiters: make(map[mediaRefreshKey]map[chan *events.MediaRetry]struct{})}

func (m *mediaRefreshManager) chatKey(chat types.JID) string {
	if m.normalizeChat != nil {
		chat = m.normalizeChat(chat)
	}
	return chat.ToNonAD().String()
}

func (m *mediaRefreshManager) handleEvent(raw any) {
	evt, ok := raw.(*events.MediaRetry)
	if !ok {
		return
	}
	key := mediaRefreshKey{m.chatKey(evt.ChatID), evt.MessageID, evt.FromMe}
	m.mu.Lock()
	defer m.mu.Unlock()
	for ch := range m.waiters[key] {
		select {
		case ch <- evt:
		default:
		}
	}
}

func (m *mediaRefreshManager) request(ctx context.Context, info *types.MessageInfo, mediaKey []byte, send func(context.Context, *types.MessageInfo, []byte) error) (string, error) {
	key := mediaRefreshKey{m.chatKey(info.Chat), info.ID, info.IsFromMe}
	ch := make(chan *events.MediaRetry, 1)
	m.mu.Lock()
	if m.waiters == nil {
		m.waiters = make(map[mediaRefreshKey]map[chan *events.MediaRetry]struct{})
	}
	if m.waiters[key] == nil {
		m.waiters[key] = make(map[chan *events.MediaRetry]struct{})
	}
	m.waiters[key][ch] = struct{}{}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.waiters[key], ch)
		if len(m.waiters[key]) == 0 {
			delete(m.waiters, key)
		}
	}()
	// Subscribe before sending: the phone may answer immediately.
	if err := send(ctx, info, mediaKey); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", fmt.Errorf("phone did not refresh the attachment in time; keep WhatsApp online and retry: %w", ctx.Err())
	case evt := <-ch:
		notif, err := whatsmeow.DecryptMediaRetryNotification(evt, mediaKey)
		if err != nil {
			return "", err
		}
		if notif.GetResult() != waMmsRetry.MediaRetryNotification_SUCCESS {
			return "", fmt.Errorf("phone could not restore attachment: %s", notif.GetResult())
		}
		return notif.GetDirectPath(), nil
	}
}
