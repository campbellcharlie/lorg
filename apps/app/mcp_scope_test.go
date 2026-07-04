package app

import (
	"errors"
	"testing"

	"github.com/campbellcharlie/lorg/lrx/rawproxy"
)

// TestEnforceSendFailClosed verifies the scope engine denies outbound requests
// unless a matching include rule is present, and that AllowAll opts out.
func TestEnforceSendFailClosed(t *testing.T) {
	type rule struct {
		typ string
		r   ScopeRule
	}

	cases := []struct {
		name      string
		policy    ScopePolicy
		rules     []rule
		url       string
		wantAllow bool
	}{
		{
			name:      "deny_empty with no rules denies",
			policy:    ScopePolicyDenyEmpty,
			url:       "https://example.com/",
			wantAllow: false,
		},
		{
			name:   "in-scope include allows matching host",
			policy: ScopePolicyDenyEmpty,
			rules: []rule{
				{typ: "include", r: ScopeRule{Protocol: "https", Host: "example.com", Port: "443"}},
			},
			url:       "https://example.com/path",
			wantAllow: true,
		},
		{
			name:   "in-scope include denies non-matching host",
			policy: ScopePolicyDenyEmpty,
			rules: []rule{
				{typ: "include", r: ScopeRule{Protocol: "https", Host: "example.com", Port: "443"}},
			},
			url:       "https://evil.example.net/path",
			wantAllow: false,
		},
		{
			name:      "allow_all with no rules allows",
			policy:    ScopePolicyAllowAll,
			url:       "https://anything.example/",
			wantAllow: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sm := NewScopeManager()
			if err := sm.SetPolicy(tc.policy); err != nil {
				t.Fatalf("SetPolicy(%s): %v", tc.policy, err)
			}
			for _, ru := range tc.rules {
				sm.AddRule(ru.typ, ru.r)
			}

			err := sm.EnforceSend(tc.url)
			if tc.wantAllow {
				if err != nil {
					t.Fatalf("EnforceSend(%q) = %v, want allow", tc.url, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("EnforceSend(%q) = nil, want deny", tc.url)
			}
			if !errors.Is(err, rawproxy.ErrOutboundDenied) {
				t.Fatalf("EnforceSend(%q) error %v does not wrap ErrOutboundDenied", tc.url, err)
			}
		})
	}
}
