package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

var silenceIDRe = regexp.MustCompile(`^[0-9A-Fa-f-]{8,64}$`)

// Alertmanager talks to the Alertmanager v2 API.
type Alertmanager struct{ c client }

func NewAlertmanager(baseURL string, rt http.RoundTripper) *Alertmanager {
	return &Alertmanager{c: newClient(baseURL, rt, 5*time.Second)}
}

type amAlert struct {
	Fingerprint string            `json:"fingerprint"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	Status      struct {
		State       string   `json:"state"`
		SilencedBy  []string `json:"silencedBy"`
		InhibitedBy []string `json:"inhibitedBy"`
	} `json:"status"`
}

// Alerts lists active, silenced and inhibited alerts, minus the always-firing
// meta alerts (Watchdog, InfoInhibitor).
func (a *Alertmanager) Alerts(ctx context.Context) ([]model.Alert, error) {
	q := url.Values{"active": {"true"}, "silenced": {"true"}, "inhibited": {"true"}}
	var raw []amAlert
	if err := a.c.getJSON(ctx, http.MethodGet, "/api/v2/alerts", q, nil, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]model.Alert, 0, len(raw))
	for _, r := range raw {
		name := r.Labels["alertname"]
		if name == "Watchdog" || name == "InfoInhibitor" {
			continue
		}
		out = append(out, model.Alert{
			Fingerprint: r.Fingerprint,
			Name:        name,
			Severity:    r.Labels["severity"],
			State:       r.Status.State,
			Summary:     r.Annotations["summary"],
			Description: r.Annotations["description"],
			Labels:      nonNilLabels(r.Labels),
			StartsAt:    r.StartsAt,
			SilencedBy:  nonNilStrings(r.Status.SilencedBy),
			InhibitedBy: nonNilStrings(r.Status.InhibitedBy),
		})
	}
	return out, nil
}

type amSilence struct {
	ID       string `json:"id"`
	Matchers []struct {
		Name    string `json:"name"`
		Value   string `json:"value"`
		IsRegex bool   `json:"isRegex"`
		IsEqual *bool  `json:"isEqual"` // absent on old Alertmanagers: equality
	} `json:"matchers"`
	CreatedBy string    `json:"createdBy"`
	Comment   string    `json:"comment"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	Status    struct {
		State string `json:"state"`
	} `json:"status"`
}

// Silences lists active and pending silences (expired ones are dropped).
func (a *Alertmanager) Silences(ctx context.Context) ([]model.Silence, error) {
	var raw []amSilence
	if err := a.c.getJSON(ctx, http.MethodGet, "/api/v2/silences", nil, nil, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]model.Silence, 0, len(raw))
	for _, r := range raw {
		if r.Status.State != "active" && r.Status.State != "pending" {
			continue
		}
		s := model.Silence{
			ID: r.ID, Matchers: make([]model.Matcher, 0, len(r.Matchers)),
			CreatedBy: r.CreatedBy, Comment: r.Comment,
			StartsAt: r.StartsAt, EndsAt: r.EndsAt, State: r.Status.State,
		}
		for _, m := range r.Matchers {
			s.Matchers = append(s.Matchers, model.Matcher{
				Name: m.Name, Value: m.Value, IsRegex: m.IsRegex,
				IsEqual: m.IsEqual == nil || *m.IsEqual,
			})
		}
		out = append(out, s)
	}
	return out, nil
}

// SilenceSpec describes a new silence; every matcher is an exact match.
type SilenceSpec struct {
	Matchers           []model.SilenceMatcher
	StartsAt, EndsAt   time.Time
	CreatedBy, Comment string
}

type amMatcherBody struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	IsRegex bool   `json:"isRegex"`
	IsEqual bool   `json:"isEqual"`
}

// CreateSilence creates a silence and returns its id.
func (a *Alertmanager) CreateSilence(ctx context.Context, s SilenceSpec) (string, error) {
	if len(s.Matchers) == 0 {
		return "", fmt.Errorf("%w: silence needs at least one matcher", ErrInvalid)
	}
	body := struct {
		Matchers  []amMatcherBody `json:"matchers"`
		StartsAt  string          `json:"startsAt"`
		EndsAt    string          `json:"endsAt"`
		CreatedBy string          `json:"createdBy"`
		Comment   string          `json:"comment"`
	}{
		Matchers:  make([]amMatcherBody, 0, len(s.Matchers)),
		StartsAt:  s.StartsAt.UTC().Format(time.RFC3339),
		EndsAt:    s.EndsAt.UTC().Format(time.RFC3339),
		CreatedBy: s.CreatedBy,
		Comment:   s.Comment,
	}
	for _, m := range s.Matchers {
		body.Matchers = append(body.Matchers, amMatcherBody{Name: m.Name, Value: m.Value, IsEqual: true})
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	var resp struct {
		SilenceID string `json:"silenceID"`
	}
	hdr := http.Header{"Content-Type": {"application/json"}}
	if err := a.c.getJSON(ctx, http.MethodPost, "/api/v2/silences", nil, bytes.NewReader(b), hdr, &resp); err != nil {
		return "", err
	}
	if resp.SilenceID == "" {
		return "", fmt.Errorf("%w: alertmanager returned no silence id", ErrInvalidResponse)
	}
	return resp.SilenceID, nil
}

// ExpireSilence expires (deletes) a silence by id.
func (a *Alertmanager) ExpireSilence(ctx context.Context, id string) error {
	if !silenceIDRe.MatchString(id) {
		return fmt.Errorf("%w: bad silence id", ErrInvalid)
	}
	return a.c.getJSON(ctx, http.MethodDelete, "/api/v2/silence/"+url.PathEscape(id), nil, nil, nil, nil)
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
