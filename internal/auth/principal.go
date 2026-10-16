package auth

import (
	"context"

	"github.com/i-got-this-faa/fbs/internal/iam"
)

type Principal struct {
	UserID      string
	DisplayName string
	AccessKeyID string
	Role        iam.Role
	DevMode     bool
	// SignedHeaders is populated for SigV4-authenticated requests.
	SignedHeaders string
	// SignedPayloadHash is the payload hash authenticated by SigV4, from the
	// canonical request's header or query. It may be UNSIGNED-PAYLOAD.
	SignedPayloadHash string
}

type principalKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
