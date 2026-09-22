package tracker

import "context"

// Forwarding of the pipeline capabilities (MarkerIndexer, EventLister,
// ChangeCursor, PlacementRecorder) through every decorator. A wrapper that
// does not re-declare an optional method hides it from the type assertion the
// caller makes, so the decorated local tracker would read as one that cannot
// index or announce anything. None of these calls is destructive or policy-
// gated: they read the index, or restate a placement the daemon derived.

// ErrPipelineUnsupported is returned when the inner provider lacks a pipeline
// capability a wrapper is asked for.
var ErrPipelineUnsupported = errPipelineUnsupported{}

type errPipelineUnsupported struct{}

func (errPipelineUnsupported) Error() string { return "tracker does not index the pipeline protocol" }

func (s *SafeProvider) ListMarkers(ctx context.Context, key string) ([]IndexedMarker, error) {
	if idx, ok := s.inner.(MarkerIndexer); ok {
		return idx.ListMarkers(ctx, key)
	}
	return nil, ErrPipelineUnsupported
}

func (s *SafeProvider) ListEvents(ctx context.Context, key string) ([]Event, error) {
	if l, ok := s.inner.(EventLister); ok {
		return l.ListEvents(ctx, key)
	}
	return nil, ErrPipelineUnsupported
}

func (s *SafeProvider) Version(ctx context.Context) (string, error) {
	if c, ok := s.inner.(ChangeCursor); ok {
		return c.Version(ctx)
	}
	return "", ErrPipelineUnsupported
}

func (s *SafeProvider) RecordPlacement(ctx context.Context, key, stage, state string) error {
	if rec, ok := s.inner.(PlacementRecorder); ok {
		return rec.RecordPlacement(ctx, key, stage, state)
	}
	return ErrPipelineUnsupported
}

func (a *AuditProvider) ListMarkers(ctx context.Context, key string) ([]IndexedMarker, error) {
	if idx, ok := a.inner.(MarkerIndexer); ok {
		return idx.ListMarkers(ctx, key)
	}
	return nil, ErrPipelineUnsupported
}

func (a *AuditProvider) ListEvents(ctx context.Context, key string) ([]Event, error) {
	if l, ok := a.inner.(EventLister); ok {
		return l.ListEvents(ctx, key)
	}
	return nil, ErrPipelineUnsupported
}

func (a *AuditProvider) Version(ctx context.Context) (string, error) {
	if c, ok := a.inner.(ChangeCursor); ok {
		return c.Version(ctx)
	}
	return "", ErrPipelineUnsupported
}

func (a *AuditProvider) RecordPlacement(ctx context.Context, key, stage, state string) error {
	if rec, ok := a.inner.(PlacementRecorder); ok {
		return rec.RecordPlacement(ctx, key, stage, state)
	}
	return ErrPipelineUnsupported
}

func (pp *PolicyProvider) ListMarkers(ctx context.Context, key string) ([]IndexedMarker, error) {
	if idx, ok := pp.inner.(MarkerIndexer); ok {
		return idx.ListMarkers(ctx, key)
	}
	return nil, ErrPipelineUnsupported
}

func (pp *PolicyProvider) ListEvents(ctx context.Context, key string) ([]Event, error) {
	if l, ok := pp.inner.(EventLister); ok {
		return l.ListEvents(ctx, key)
	}
	return nil, ErrPipelineUnsupported
}

func (pp *PolicyProvider) Version(ctx context.Context) (string, error) {
	if c, ok := pp.inner.(ChangeCursor); ok {
		return c.Version(ctx)
	}
	return "", ErrPipelineUnsupported
}

func (pp *PolicyProvider) RecordPlacement(ctx context.Context, key, stage, state string) error {
	if rec, ok := pp.inner.(PlacementRecorder); ok {
		return rec.RecordPlacement(ctx, key, stage, state)
	}
	return ErrPipelineUnsupported
}

func (d *DestructiveProvider) ListMarkers(ctx context.Context, key string) ([]IndexedMarker, error) {
	if idx, ok := d.inner.(MarkerIndexer); ok {
		return idx.ListMarkers(ctx, key)
	}
	return nil, ErrPipelineUnsupported
}

func (d *DestructiveProvider) ListEvents(ctx context.Context, key string) ([]Event, error) {
	if l, ok := d.inner.(EventLister); ok {
		return l.ListEvents(ctx, key)
	}
	return nil, ErrPipelineUnsupported
}

func (d *DestructiveProvider) Version(ctx context.Context) (string, error) {
	if c, ok := d.inner.(ChangeCursor); ok {
		return c.Version(ctx)
	}
	return "", ErrPipelineUnsupported
}

func (d *DestructiveProvider) RecordPlacement(ctx context.Context, key, stage, state string) error {
	if rec, ok := d.inner.(PlacementRecorder); ok {
		return rec.RecordPlacement(ctx, key, stage, state)
	}
	return ErrPipelineUnsupported
}
