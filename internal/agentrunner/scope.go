package agentrunner

import "context"

type toolRootKey struct{}

// WithToolRoot keeps the policy's write scope separate from the working
// directory used to resolve ticket/spec paths in phase prompts.
func WithToolRoot(ctx context.Context, root string) context.Context {
	return context.WithValue(ctx, toolRootKey{}, root)
}
func toolRoot(ctx context.Context, fallback string) string {
	if root, ok := ctx.Value(toolRootKey{}).(string); ok {
		return root
	}
	return fallback
}
