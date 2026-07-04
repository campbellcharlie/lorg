package rawproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"sync"
)

type OutboundDecisionInput struct {
	Scheme string
	Host   string
	Port   string
	Path   string
	Tool   string
}

type OutboundAuthorizer func(context.Context, OutboundDecisionInput) error

var outboundAuthorizer struct {
	mu sync.RWMutex
	fn OutboundAuthorizer
}

var ErrOutboundDenied = errors.New("outbound request denied by scope")

func SetOutboundAuthorizer(fn OutboundAuthorizer) {
	outboundAuthorizer.mu.Lock()
	defer outboundAuthorizer.mu.Unlock()
	outboundAuthorizer.fn = fn
}

func AuthorizeOutbound(ctx context.Context, in OutboundDecisionInput) error {
	outboundAuthorizer.mu.RLock()
	fn := outboundAuthorizer.fn
	outboundAuthorizer.mu.RUnlock()
	if fn == nil {
		// Fail closed: with no authorizer registered we cannot prove the
		// request is in scope, so deny it rather than leak an outbound call.
		return fmt.Errorf("%w: no scope authorizer registered", ErrOutboundDenied)
	}
	return fn(ctx, in)
}

func AuthorizeOutboundURL(ctx context.Context, rawURL, tool string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	return AuthorizeOutbound(ctx, OutboundDecisionInput{
		Scheme: u.Scheme,
		Host:   u.Hostname(),
		Port:   portForURL(u),
		Path:   u.EscapedPath(),
		Tool:   tool,
	})
}

func AuthorizeOutboundDial(ctx context.Context, scheme, addr, tool string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return AuthorizeOutbound(ctx, OutboundDecisionInput{
		Scheme: scheme,
		Host:   host,
		Port:   port,
		Path:   "/",
		Tool:   tool,
	})
}

func portForURL(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch u.Scheme {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	if _, err := strconv.Atoi(u.Host); err == nil {
		return u.Host
	}
	return ""
}
