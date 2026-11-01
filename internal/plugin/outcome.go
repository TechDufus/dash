package plugin

// ActionTarget is the object an action ran on: kind (e.g. "Silence"), namespace and name, as the
// audit log records them.
type ActionTarget struct {
	Kind, Namespace, Name string
}

// Outcome is what Act returns when the audit target only exists after the action ran (a created
// silence has no ID before). The core answers 200 with Body (202 when Body is nil) and, on
// success only, replaces the target of the audit record it wrote before Act with Target. A
// failed action keeps the target ActionSpec.Target gave. Act may return an Outcome or a pointer
// to one.
type Outcome struct {
	Body   any
	Target *ActionTarget
}
