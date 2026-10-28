package main

import (
	"fmt"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// groupRequestTimeout bounds each group management call to WhatsApp.
const groupRequestTimeout = 30 * time.Second

// parseGroupJID parses a group or community JID and rejects anything that is
// not on the group server. types.ParseJID accepts "" and bare numbers, which
// would otherwise reach whatsmeow as an empty or user JID.
func parseGroupJID(raw string) (types.JID, error) {
	if raw == "" {
		return types.JID{}, fmt.Errorf("group JID is required")
	}
	jid, err := types.ParseJID(raw)
	if err != nil {
		return types.JID{}, err
	}
	if jid.Server != types.GroupServer || jid.User == "" {
		return types.JID{}, fmt.Errorf("%q is not a group JID (expected ...@g.us)", raw)
	}
	return jid, nil
}
