package secret

import (
	"context"
	"log/slog"
)

// NewHandler wraps next so a secret that leaks into third-party error text
// never reaches the log (design C.6). It scrubs the message and every string
// attribute, including attributes inside groups, LogValuer results and the
// text of error values. Attributes of other types (structs, maps) are passed
// through untouched; log those as strings or Values.
func NewHandler(next slog.Handler, reg *Registry) slog.Handler {
	return &handler{next: next, reg: reg}
}

type handler struct {
	next slog.Handler
	reg  *Registry
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, h.reg.Scrub(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = h.attr(a)
	}
	return &handler{next: h.next.WithAttrs(clean), reg: h.reg}
}

func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{next: h.next.WithGroup(name), reg: h.reg}
}

func (h *handler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(h.reg.Scrub(v.String()))
	case slog.KindGroup:
		group := v.Group()
		clean := make([]slog.Attr, len(group))
		for i, ga := range group {
			clean[i] = h.attr(ga)
		}
		a.Value = slog.GroupValue(clean...)
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			a.Value = h.errorValue(err)
		} else {
			a.Value = v
		}
	default:
		a.Value = v
	}
	return a
}

// errorValue keeps the original error (errors.Is and As still work for any
// handler that cares) but makes its text clean. An error that needs no
// scrubbing is passed through unchanged.
func (h *handler) errorValue(err error) slog.Value {
	if err == nil {
		return slog.AnyValue(err)
	}
	msg := err.Error()
	clean := h.reg.Scrub(msg)
	if clean == msg {
		return slog.AnyValue(err)
	}
	return slog.AnyValue(&scrubbedError{msg: clean, err: err})
}

type scrubbedError struct {
	msg string
	err error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.err }
