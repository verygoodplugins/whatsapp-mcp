package main

import (
	"testing"
	"time"
)

func TestStoreMessagePreservesAudioTranscriptOnReplay(t *testing.T) {
	for _, tc := range []struct{ name, media, incoming, want string }{
		{"audio replay", "audio", "", "[transcript (openai_compatible parakeet)] hello"},
		{"human caption", "audio", "new caption", "new caption"},
		{"other media", "image", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := newTestMessageStore(t)
			chat := "test@g.us"
			save := func(content, media string) {
				t.Helper()
				if err := ms.StoreMessage("voice", chat, "sender", content, time.Now(), false,
					media, "", "", nil, nil, nil, 0, ""); err != nil {
					t.Fatal(err)
				}
			}
			save("[transcript (openai_compatible parakeet)] hello", "audio")
			save(tc.incoming, tc.media)
			var got string
			if err := ms.db.QueryRow("SELECT content FROM messages WHERE id = ? AND chat_jid = ?", "voice", chat).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("content = %q, want %q", got, tc.want)
			}
		})
	}
}
