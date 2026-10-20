package main

import (
	"testing"
	"time"
)

func TestGetChatName_RepairsStoredSelfNumber(t *testing.T) {
	for _, name := range []string{selfPhone.User, selfLID.User} {
		t.Run(name, func(t *testing.T) {
			client := newTestClientWithSelf(&mockLIDStore{}, selfPhone)
			client.Store.LID = selfLID
			ms := newTestMessageStore(t)
			if err := ms.StoreChat(phonePN.String(), name, time.Now()); err != nil {
				t.Fatal(err)
			}
			got := GetChatName(client, ms, phonePN, phonePN.String(), nil, selfPhone.User, testLogger())
			if got != phonePN.User {
				t.Fatalf("GetChatName() = %q, want peer number %q", got, phonePN.User)
			}
		})
	}
}
