package billing

import "context"

type actorKey struct{}

func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}
func Actor(ctx context.Context) string { v, _ := ctx.Value(actorKey{}).(string); return v }
