package main

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestGroupInfoResponse(t *testing.T) {
	pn := types.NewJID("15555550100", types.DefaultUserServer)
	lid := types.NewJID("123456789", types.HiddenUserServer)
	unresolved := types.NewJID("987654321", types.HiddenUserServer)
	info := &types.GroupInfo{
		JID:       types.NewJID("120363000000000000", types.GroupServer),
		GroupName: types.GroupName{Name: "Family"},
		Participants: []types.GroupParticipant{
			{JID: lid, PhoneNumber: pn, IsAdmin: true},
			{JID: unresolved},
		},
	}
	got := groupInfoResponse(info, func(j, alt types.JID) types.JID { return resolveUserJID(nil, j, alt) })
	if got.Name != "Family" || got.JID != "120363000000000000@g.us" {
		t.Fatalf("group = %q %q", got.JID, got.Name)
	}
	if len(got.Participants) != 2 {
		t.Fatalf("participants = %d, want 2", len(got.Participants))
	}
	if p := got.Participants[0]; p.JID != "15555550100@s.whatsapp.net" || p.Phone != "15555550100" || !p.IsAdmin {
		t.Fatalf("LID member with a phone number = %+v", p)
	}
	if p := got.Participants[1]; p.JID != "987654321@lid" || p.Phone != "" {
		t.Fatalf("unresolved LID member = %+v", p)
	}
}
