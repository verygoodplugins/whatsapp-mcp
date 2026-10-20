package main

import "testing"

func TestExtractVCardPhones_UnfoldsLines(t *testing.T) {
	for _, card := range []string{
		"TEL;TYPE=CELL:\r\n +12025550123\r\n",
		"TEL:+1202\r\n 5550123\r\n",
		"TEL:+1202\r\n\t5550123\r\n",
		"TEL:+1202\n 5550123\n",
	} {
		phones := extractVCardPhones(card)
		if len(phones) != 1 || phones[0] != "+12025550123" {
			t.Fatalf("extractVCardPhones(%q) = %v, want complete phone number", card, phones)
		}
	}
}
