package world

import (
	"context"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

var logMsgs = []string{
	`level=info msg="request served" method=GET path=/healthz status=200 duration=1.2ms`,
	`level=info msg="reconcile finished" changed=0 duration=48ms`,
	`level=info msg="cache synced" kind=Pod items=212`,
	`level=debug msg="watch event" type=MODIFIED object=demo/web-7d9c`,
	`level=info msg="request served" method=GET path=/api/stream status=200 duration=31.4s`,
	`level=warn msg="slow upstream" target=metrics duration=1.8s`,
}

// LogLine is the i-th synthetic log line of a container, stamped at. Empty pod and container
// names read as "demo" and "app".
func LogLine(pod, container string, at time.Time, i int) model.LogLine {
	if pod == "" {
		pod = "demo"
	}
	if container == "" {
		container = "app"
	}
	return model.LogLine{Time: at.UTC(), Pod: pod, Container: container, Msg: logMsgs[i%len(logMsgs)]}
}

// Logs is a few minutes of plausible lines for a pod, oldest first, ending now.
func (w *World) Logs(pod, container string) []model.LogLine {
	return LogsAt(pod, container, w.clock())
}

// LogsAt is Logs for a clock reading, for a caller that has no World (the demo twin of the
// victorialogs plugin).
func LogsAt(pod, container string, now time.Time) []model.LogLine {
	lines := make([]model.LogLine, 0, 60)
	for i := 59; i >= 0; i-- {
		lines = append(lines, LogLine(pod, container, now.Add(-time.Duration(i)*7*time.Second), i))
	}
	return lines
}

// TailLogs emits a new synthetic line every interval, stamped with now(), until ctx ends
// (returns nil) or emit fails.
func TailLogs(ctx context.Context, pod, container string, interval time.Duration, now func() time.Time, emit func(model.LogLine) error) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := emit(LogLine(pod, container, now(), i)); err != nil {
				return err
			}
		}
	}
}
