package feed

import (
	"errors"
	"testing"
)

func TestVerifyUnsigned(t *testing.T) {
	k, stranger := newKey(t), newKey(t)
	keys := NewTrustedKeys(pub(k))
	data := fixture(t)
	good := Sign(data, k).Marshal()
	bad := Sign(data, stranger)
	bad.KeyID = KeyID(pub(k))

	for _, tc := range []struct {
		name         string
		keys         TrustedKeys
		sig          []byte
		allow        bool
		wantErr      error
		wantUnsigned bool
	}{
		{name: "no signature, not allowed", keys: keys, wantErr: ErrBadSignature},
		{name: "no signature, no keys, not allowed", wantErr: ErrUnknownKey},
		{name: "no signature, allowed", keys: keys, allow: true, wantUnsigned: true},
		{name: "no signature, no keys, allowed", allow: true, wantUnsigned: true},
		{name: "signature, no keys, allowed", sig: good, allow: true, wantUnsigned: true},
		{name: "good signature, allowed", keys: keys, sig: good, allow: true},
		{name: "bad signature still rejected when allowed", keys: keys, sig: bad.Marshal(), allow: true, wantErr: ErrBadSignature},
		{name: "unknown key still rejected when allowed", keys: keys, sig: Sign(data, stranger).Marshal(), allow: true, wantErr: ErrUnknownKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Verify(data, tc.sig, VerifyOptions{Keys: tc.keys, Now: testNow, AllowUnsigned: tc.allow})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if m.Unsigned != tc.wantUnsigned {
				t.Fatalf("Unsigned = %v, want %v", m.Unsigned, tc.wantUnsigned)
			}
		})
	}
}

func TestUnsignedFeedAllowedOnlyWithoutProductionKey(t *testing.T) {
	if want := ProductionKeyCurrent == "" && ProductionKeyNext == ""; UnsignedFeedAllowed() != want {
		t.Fatalf("UnsignedFeedAllowed() = %v with production keys %q, %q", !want, ProductionKeyCurrent, ProductionKeyNext)
	}
}
