package keymat

import "testing"

func BenchmarkHash(b *testing.B) {
	plaintext := "ck_live_ABCDEFGHIJKLMNOPQRSTUVWXYZ234567ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	for b.Loop() {
		_ = Hash(plaintext)
	}
}

func BenchmarkValidate(b *testing.B) {
	plaintext := "ck_live_ABCDEFGHIJKLMNOPQRSTUVWXYZ234567ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	for b.Loop() {
		if err := Validate(plaintext, "ck_live_"); err != nil {
			b.Fatal(err)
		}
	}
}
