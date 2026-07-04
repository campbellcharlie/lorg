package rawproxy

import (
	"context"
	"errors"
	"testing"
)

// TestAuthorizeOutboundNilFailsClosed verifies that with no authorizer
// registered, outbound authorization denies the request instead of allowing it.
func TestAuthorizeOutboundNilFailsClosed(t *testing.T) {
	// Ensure a clean, unregistered state and restore afterwards.
	SetOutboundAuthorizer(nil)
	t.Cleanup(func() { SetOutboundAuthorizer(nil) })

	err := AuthorizeOutboundURL(context.Background(), "https://example.com/", "test")
	if err == nil {
		t.Fatal("AuthorizeOutboundURL with no authorizer returned nil, want deny")
	}
	if !errors.Is(err, ErrOutboundDenied) {
		t.Fatalf("error %v does not wrap ErrOutboundDenied", err)
	}
}

// TestAuthorizeOutboundDelegates verifies the registered authorizer decides.
func TestAuthorizeOutboundDelegates(t *testing.T) {
	SetOutboundAuthorizer(func(_ context.Context, in OutboundDecisionInput) error {
		if in.Host == "allowed.example" {
			return nil
		}
		return ErrOutboundDenied
	})
	t.Cleanup(func() { SetOutboundAuthorizer(nil) })

	if err := AuthorizeOutboundURL(context.Background(), "https://allowed.example/", "test"); err != nil {
		t.Fatalf("allowed host denied: %v", err)
	}
	if err := AuthorizeOutboundURL(context.Background(), "https://blocked.example/", "test"); err == nil {
		t.Fatal("blocked host allowed, want deny")
	}
}
