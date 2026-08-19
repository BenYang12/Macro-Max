package server

import (
	"strings"
	"testing"

	"github.com/BenYang12/Macro-Max/internal/kroger"
)

// WEB_APP_URL is required only where it is actually used: the Kroger cart
// routes, whose callback redirects the browser back to that origin. Config
// deliberately no longer enforces it, so this is the test that keeps a
// misconfigured API server from starting with a cart flow that cannot
// complete. Registering the cart routes must fail loudly at startup rather
// than at the first user click.
func TestNewRequiresWebAppURLWhenKrogerConfigured(t *testing.T) {
	_, err := New(Deps{
		Addr:               ":0",
		Kroger:             kroger.New("id", "secret"),
		KrogerClientSecret: "secret",
		WebAppURL:          "",
	})
	if err == nil {
		t.Fatal("expected server.New to reject Kroger credentials without WEB_APP_URL")
	}
	if !strings.Contains(err.Error(), "WEB_APP_URL") {
		t.Errorf("error should name the missing setting, got: %v", err)
	}
}

// The mirror image: no Kroger client means no cart routes, so WEB_APP_URL is
// irrelevant and must not block startup. This is the shape cmd/api runs in
// when KROGER_CLIENT_ID is unset.
func TestNewWithoutKrogerIgnoresWebAppURL(t *testing.T) {
	if _, err := New(Deps{Addr: ":0"}); err != nil {
		t.Fatalf("server without Kroger must start: %v", err)
	}
}
