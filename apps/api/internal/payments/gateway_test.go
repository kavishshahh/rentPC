package payments

import (
	"testing"
	"time"
)

func TestPrepaidSignVerify(t *testing.T) {
	c := PrepaidClaim{Provider: "mpp", Ref: "0xabc", AmountCents: 150, Currency: "USD", ListingID: "l1", Hours: 3}
	h, err := SignPrepaid("secret", c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyPrepaid("secret", h)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ref != "0xabc" || got.AmountCents != 150 || got.Hours != 3 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if _, err := VerifyPrepaid("other", h); err == nil {
		t.Fatal("wrong secret must fail")
	}
	if _, err := VerifyPrepaid("", h); err == nil {
		t.Fatal("unconfigured secret must fail")
	}
	old := c
	old.IssuedAt = time.Now().Add(-10 * time.Minute).Unix()
	h2, _ := SignPrepaid("secret", old)
	if _, err := VerifyPrepaid("secret", h2); err == nil {
		t.Fatal("stale claim must fail")
	}
}
