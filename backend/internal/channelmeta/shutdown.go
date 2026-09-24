package channelmeta

import "context"

// parentKey carries the worker's loop context underneath the stall-capped one
// handed to Resolve. The cap cancels the child, a process shutdown cancels the
// parent, and Resolve has to tell the two apart: a stall is an attempt that
// must be recorded, a shutdown is not.
type parentKey struct{}

// withParent records parent as the shutdown authority for ctx.
func withParent(ctx, parent context.Context) context.Context {
	return context.WithValue(ctx, parentKey{}, parent)
}

// interruptedByShutdown reports whether the loop context beneath ctx is
// cancelled. Contexts without a recorded parent (the HTTP callers of Resolve)
// are never "shut down": their cancellation is a client going away, and the
// attempt is recorded as usual.
func interruptedByShutdown(ctx context.Context) bool {
	parent, _ := ctx.Value(parentKey{}).(context.Context)
	return parent != nil && parent.Err() != nil
}
