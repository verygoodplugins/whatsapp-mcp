package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
)

type fakeAppStateClient struct {
	sendErrs  []error // returned by successive SendAppState calls
	sends     int
	fullSyncs []appstate.WAPatchName
	syncErr   error

	version      uint64
	recoveries   int
	phoneVersion uint64 // version the phone's snapshot brings; 0 = phone never answers
}

func (f *fakeAppStateClient) RequestRecovery(_ context.Context, _ appstate.WAPatchName) error {
	f.recoveries++
	f.version = f.phoneVersion
	return nil
}

func (f *fakeAppStateClient) AppStateVersion(_ context.Context, _ appstate.WAPatchName) (uint64, error) {
	return f.version, nil
}

func (f *fakeAppStateClient) SendAppState(_ context.Context, _ appstate.PatchInfo) error {
	f.sends++
	if len(f.sendErrs) == 0 {
		return nil
	}
	err := f.sendErrs[0]
	f.sendErrs = f.sendErrs[1:]
	return err
}

func (f *fakeAppStateClient) FetchAppState(_ context.Context, name appstate.WAPatchName, fullSync, _ bool) error {
	if fullSync {
		f.fullSyncs = append(f.fullSyncs, name)
	}
	return f.syncErr
}

// The error whatsmeow returns when the stored state has diverged: a 409 whose
// conflicting patches don't verify against the stored LTHash.
var divergedErr = fmt.Errorf("%w (regular_high): <error code=\"409\" text=\"conflict\"/> (also, applying patches in the response failed: %w)",
	whatsmeow.ErrAppStateUpdate, fmt.Errorf("failed to verify patch v148: %w", appstate.ErrMismatchingLTHash))

func noLog(string, ...any) {}

func TestSendAppStateWithResync(t *testing.T) {
	patch := appstate.PatchInfo{Type: appstate.WAPatchRegularHigh}

	t.Run("success needs no resync", func(t *testing.T) {
		f := &fakeAppStateClient{}
		if err := sendAppStateWithResync(context.Background(), f, patch, noLog); err != nil {
			t.Fatal(err)
		}
		if f.sends != 1 || len(f.fullSyncs) != 0 {
			t.Fatalf("sends=%d fullSyncs=%v", f.sends, f.fullSyncs)
		}
	})

	t.Run("diverged state resyncs then retries once", func(t *testing.T) {
		f := &fakeAppStateClient{sendErrs: []error{divergedErr, divergedErr}}
		if err := sendAppStateWithResync(context.Background(), f, patch, noLog); err != nil {
			t.Fatal(err)
		}
		if f.sends != 3 || len(f.fullSyncs) != 1 || f.fullSyncs[0] != appstate.WAPatchRegularHigh {
			t.Fatalf("sends=%d fullSyncs=%v", f.sends, f.fullSyncs)
		}
	})

	t.Run("retry failure is returned, no second resync", func(t *testing.T) {
		f := &fakeAppStateClient{sendErrs: []error{divergedErr, divergedErr, divergedErr}}
		err := sendAppStateWithResync(context.Background(), f, patch, noLog)
		if !errors.Is(err, appstate.ErrMismatchingLTHash) {
			t.Fatalf("err = %v", err)
		}
		if f.sends != 3 || len(f.fullSyncs) != 1 {
			t.Fatalf("sends=%d fullSyncs=%v", f.sends, f.fullSyncs)
		}
	})

	t.Run("resync failure skips the retry", func(t *testing.T) {
		f := &fakeAppStateClient{sendErrs: []error{divergedErr, divergedErr}, syncErr: errors.New("offline")}
		if err := sendAppStateWithResync(context.Background(), f, patch, noLog); err == nil {
			t.Fatal("want error")
		}
		if f.sends != 2 || f.recoveries != 0 {
			t.Fatalf("sends=%d recoveries=%d", f.sends, f.recoveries)
		}
	})

	shortRecovery(t)
	lthashErr := fmt.Errorf("failed to verify patch v148: %w", appstate.ErrMismatchingLTHash)

	t.Run("server chain broken: recover from the phone, then retry", func(t *testing.T) {
		f := &fakeAppStateClient{sendErrs: []error{divergedErr, divergedErr}, syncErr: lthashErr, phoneVersion: 148}
		if err := sendAppStateWithResync(context.Background(), f, patch, noLog); err != nil {
			t.Fatal(err)
		}
		if f.recoveries != 1 || f.sends != 3 {
			t.Fatalf("sends=%d recoveries=%d", f.sends, f.recoveries)
		}
	})

	t.Run("phone never answers: give up without retrying", func(t *testing.T) {
		f := &fakeAppStateClient{sendErrs: []error{divergedErr, divergedErr}, syncErr: lthashErr}
		err := sendAppStateWithResync(context.Background(), f, patch, noLog)
		if err == nil || f.recoveries != 1 || f.sends != 2 {
			t.Fatalf("err=%v sends=%d recoveries=%d", err, f.sends, f.recoveries)
		}
	})

	t.Run("repaired by a concurrent request while waiting: no resync", func(t *testing.T) {
		f := &fakeAppStateClient{sendErrs: []error{divergedErr}}
		if err := sendAppStateWithResync(context.Background(), f, patch, noLog); err != nil {
			t.Fatal(err)
		}
		if f.sends != 2 || len(f.fullSyncs) != 0 {
			t.Fatalf("sends=%d fullSyncs=%v", f.sends, f.fullSyncs)
		}
	})

	notRepaired := map[string]error{
		"not connected":      whatsmeow.ErrNotConnected,
		"other server error": fmt.Errorf("%w (regular_high): <error code=\"400\" text=\"bad-request\"/>", whatsmeow.ErrAppStateUpdate),
		// The server accepted the patch; only the follow-up fetch failed.
		"post-send fetch": fmt.Errorf("failed to fetch app state after sending update: %w", lthashErr),
	}
	for name, sendErr := range notRepaired {
		t.Run("not repaired: "+name, func(t *testing.T) {
			f := &fakeAppStateClient{sendErrs: []error{sendErr}}
			if err := sendAppStateWithResync(context.Background(), f, patch, noLog); !errors.Is(err, sendErr) {
				t.Fatalf("err = %v", err)
			}
			if f.sends != 1 || len(f.fullSyncs) != 0 || f.recoveries != 0 {
				t.Fatalf("sends=%d fullSyncs=%v recoveries=%d", f.sends, f.fullSyncs, f.recoveries)
			}
		})
	}
}

func shortRecovery(t *testing.T) {
	t.Helper()
	timeout, poll := appStateRecoveryTimeout, appStateRecoveryPoll
	appStateRecoveryTimeout, appStateRecoveryPoll = 50*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { appStateRecoveryTimeout, appStateRecoveryPoll = timeout, poll })
}
