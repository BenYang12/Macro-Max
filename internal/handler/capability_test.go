package handler

import (
	"crypto/sha256"
	"net/http/httptest"
	"testing"
)

const (
	testCapabilityToken      = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	testWrongCapabilityToken = "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"
)

func testCapabilityDigest() []byte {
	digest := sha256.Sum256(make([]byte, capabilityBytes))
	return digest[:]
}

func TestCapabilityDigest_AcceptsCaseInsensitiveBearerScheme(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "bearer "+testCapabilityToken)
	got, ok := capabilityDigest(req)
	if !ok {
		t.Fatal("lowercase bearer scheme was rejected")
	}
	if string(got) != string(testCapabilityDigest()) {
		t.Fatal("lowercase bearer scheme produced the wrong digest")
	}
}
