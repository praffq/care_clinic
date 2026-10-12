package main

import (
	"strings"
	"testing"
)

func TestValidatePasswordHonorsBcryptByteLimit(t *testing.T) {
	tooManyBytes := strings.Repeat("𐐀", 18) + "a1"
	if len([]rune(tooManyBytes)) > 20 || len(tooManyBytes) <= 72 {
		t.Fatalf("test password does not isolate the UTF-8 byte limit: runes=%d bytes=%d",
			len([]rune(tooManyBytes)), len(tooManyBytes))
	}
	if err := ValidatePassword(tooManyBytes); err == nil || !strings.Contains(err.Error(), "72 UTF-8 bytes") {
		t.Fatalf("overlong UTF-8 password error = %v", err)
	}

	withinLimit := strings.Repeat("𐐀", 17) + "a1B"
	if len(withinLimit) > 72 {
		t.Fatalf("test password exceeds byte limit: %d", len(withinLimit))
	}
	if err := ValidatePassword(withinLimit); err != nil {
		t.Fatalf("valid password under bcrypt byte limit rejected: %v", err)
	}
}
