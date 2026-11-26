package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
)

// appStateClient is what sendAppStateWithResync needs from the WhatsApp
// client, so tests can fake it. whatsmeowAppState adapts *whatsmeow.Client.
type appStateClient interface {
	SendAppState(ctx context.Context, patch appstate.PatchInfo) error
	FetchAppState(ctx context.Context, name appstate.WAPatchName, fullSync, onlyIfNotSynced bool) error
	// RequestRecovery asks the phone to send its own copy of the collection.
	RequestRecovery(ctx context.Context, name appstate.WAPatchName) error
	// AppStateVersion is the collection's stored version (0 = none).
	AppStateVersion(ctx context.Context, name appstate.WAPatchName) (uint64, error)
}

type whatsmeowAppState struct{ *whatsmeow.Client }

func (c whatsmeowAppState) RequestRecovery(ctx context.Context, name appstate.WAPatchName) error {
	_, err := c.SendPeerMessage(ctx, whatsmeow.BuildAppStateRecoveryRequest(name))
	return err
}

func (c whatsmeowAppState) AppStateVersion(ctx context.Context, name appstate.WAPatchName) (uint64, error) {
	version, _, err := c.Store.AppState.GetAppStateVersion(ctx, string(name))
	return version, err
}

// Timing of a repair. The whole repair stays under the bridge's 60s HTTP
// WriteTimeout, so the caller still gets the answer.
var (
	appStateRepairBudget    = 50 * time.Second
	appStateRecoveryTimeout = 30 * time.Second // wait for the phone's snapshot
	appStateRecoveryPoll    = time.Second
)

// appStateRepairMu runs one repair at a time: concurrent sends on a broken
// collection would otherwise each wipe and refetch it, and a send encoded
// while another resync has the version row deleted fails spuriously.
var appStateRepairMu sync.Mutex

// appStateDiverged reports whether the server rejected a send and the
// patches it returned don't verify against the stored state ("failed to
// verify patch vN: mismatching LTHash"). whatsmeow's own retry applies those
// patches on top of the stored state, so every later send fails the same way.
//
// Both parts are required. Other server errors (4xx, rate limits) are not
// fixed by a resync, and an LTHash error without ErrAppStateUpdate comes from
// the fetch whatsmeow runs after the server already accepted the patch.
func appStateDiverged(err error) bool {
	return errors.Is(err, whatsmeow.ErrAppStateUpdate) && errors.Is(err, appstate.ErrMismatchingLTHash)
}

// sendAppStateWithResync sends an app-state patch and, if the collection has
// diverged, repairs it and retries the send once. The device stays linked.
//
// Repair is a full resync from the server's snapshot first. If the server's
// own patch chain doesn't verify either (seen with regular_high v148, where a
// clean snapshot + patches still fails the LTHash check), it asks the phone
// for its copy of the collection (whatsmeow's fatal-recovery request) and
// waits for that to land in the store. Both repairs keep the device linked.
func sendAppStateWithResync(ctx context.Context, client appStateClient, patch appstate.PatchInfo, logf func(string, ...any)) error {
	err := client.SendAppState(ctx, patch)
	if err == nil || !appStateDiverged(err) {
		return err
	}
	appStateRepairMu.Lock()
	defer appStateRepairMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, appStateRepairBudget)
	defer cancel()
	// Another request may have repaired the collection while this one waited.
	if err = client.SendAppState(ctx, patch); err == nil || !appStateDiverged(err) {
		return err
	}
	logf("App state %s diverged from the server (%v); doing a full resync", patch.Type, err)
	if syncErr := client.FetchAppState(ctx, patch.Type, true, false); syncErr != nil {
		if !errors.Is(syncErr, appstate.ErrMismatchingLTHash) {
			return fmt.Errorf("%w (full resync of %s also failed: %w)", err, patch.Type, syncErr)
		}
		logf("Full resync of %s failed too (%v); asking the phone for a recovery snapshot", patch.Type, syncErr)
		if recErr := recoverAppStateFromPhone(ctx, client, patch.Type); recErr != nil {
			return fmt.Errorf("%w (full resync of %s failed: %w; recovery from the phone failed: %w)", err, patch.Type, syncErr, recErr)
		}
	}
	if retryErr := client.SendAppState(ctx, patch); retryErr != nil {
		return fmt.Errorf("after repairing %s: %w", patch.Type, retryErr)
	}
	logf("App state %s repaired; send succeeded on retry", patch.Type)
	return nil
}

// recoverAppStateFromPhone sends the recovery request and waits until the
// phone's snapshot has replaced the stored version (whatsmeow applies it when
// the peer response arrives, in the message handler).
func recoverAppStateFromPhone(ctx context.Context, client appStateClient, name appstate.WAPatchName) error {
	before, err := client.AppStateVersion(ctx, name)
	if err != nil {
		return err
	}
	if err := client.RequestRecovery(ctx, name); err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	deadline := time.NewTimer(appStateRecoveryTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(appStateRecoveryPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("no snapshot from the phone within %s (is it online?)", appStateRecoveryTimeout)
		case <-tick.C:
			version, err := client.AppStateVersion(ctx, name)
			if err != nil {
				return err
			}
			if version > before {
				return nil
			}
		}
	}
}
