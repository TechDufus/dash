package alertmanager

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/upstream"
)

const (
	actionCreate = "silence.create"
	actionExpire = "silence.expire"

	maxMatchers = 20
	maxComment  = 500
	// maxMatcherName and maxMatcherValue bound a matcher (bytes): a label name is short, and a
	// value long enough for any real label still keeps a request, its summary and its audit
	// target small.
	maxMatcherName  = 256
	maxMatcherValue = 1024
)

// silenceLengths is what each duration id of plugins.alertmanager.silenceDurations means.
var silenceLengths = map[string]time.Duration{
	"1h":  time.Hour,
	"4h":  4 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
}

// silenceCreateInput is the body of silence.create: the request type the SPA already sends, with
// the static checks that need no configuration. The matchers carry a name and a value only, and
// the upstream client creates every one as an exact match (isEqual, no regex), so a negated or
// regex matcher cannot be requested through dash. The test pinning the matcher's fields makes
// whoever adds an operator revisit the protected-alert rule below.
type silenceCreateInput struct{ model.SilenceRequest }

// Validate holds the checks that need no configuration; the duration is checked in Act, against
// the instance's silenceDurations.
func (in *silenceCreateInput) Validate() error {
	if n := len(in.Matchers); n < 1 || n > maxMatchers {
		return errors.New("need between 1 and 20 matchers")
	}
	hasAlertname := false
	for _, m := range in.Matchers {
		if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Value) == "" {
			return errors.New("matcher name and value must not be empty")
		}
		if len(m.Name) > maxMatcherName || len(m.Value) > maxMatcherValue {
			return fmt.Errorf("a matcher name is at most %d bytes and a value at most %d", maxMatcherName, maxMatcherValue)
		}
		hasAlertname = hasAlertname || m.Name == "alertname"
	}
	if !hasAlertname {
		return errors.New("an alertname matcher is required")
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(in.Comment)); n < 1 || n > maxComment {
		return errors.New("comment must be 1-500 characters")
	}
	return nil
}

// alertname is the value of the first alertname matcher: what the audit intent names, since the
// silence has no id yet.
func (in *silenceCreateInput) alertname() string {
	for _, m := range in.Matchers {
		if m.Name == "alertname" {
			return m.Value
		}
	}
	return ""
}

// silenceExpireInput is the body of silence.expire.
type silenceExpireInput struct {
	ID string `json:"id" dash:"required" doc:"The id of the silence to expire. Only silences dash created can be expired."`
}

func (in *silenceExpireInput) Validate() error {
	if !upstream.ValidSilenceID(in.ID) {
		return errors.New("id is not a silence id")
	}
	return nil
}

func actionSpecs() []plugin.ActionSpec {
	return []plugin.ActionSpec{
		{
			Name: actionCreate, Title: "Silence", MinRole: auth.RoleAdmin, Confirm: plugin.ConfirmNone,
			Input: func() any { return &silenceCreateInput{} },
			Summary: func(in any) string {
				i := in.(*silenceCreateInput)
				return fmt.Sprintf("Silence %s for %s", i.alertname(), i.Duration)
			},
			// The silence has no id until Alertmanager makes one: the intent names the alert, and
			// the outcome (plugin.Outcome) the silence.
			Target: func(in any) (string, string, string) { return "Silence", "", in.(*silenceCreateInput).alertname() },
		},
		{
			Name: actionExpire, Title: "Expire silence", MinRole: auth.RoleAdmin, Confirm: plugin.ConfirmNone, Idempotent: true,
			Input:   func() any { return &silenceExpireInput{} },
			Summary: func(in any) string { return "Expire silence " + in.(*silenceExpireInput).ID },
			Target:  func(in any) (string, string, string) { return "Silence", "", in.(*silenceExpireInput).ID },
		},
	}
}

func refuse(status int, code, format string, args ...any) *plugin.ActionError {
	return &plugin.ActionError{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// planSilence applies the rules that depend on the configuration to a request whose static
// checks passed, and returns the length and the trimmed comment of the silence to create. The
// duration must be one the instance offers, and no alertname matcher may name a protected alert.
func planSilence(c *pluginconf.Alertmanager, in *silenceCreateInput) (time.Duration, string, *plugin.ActionError) {
	dur, ok := silenceLengths[in.Duration]
	if !ok || !slices.Contains(c.SilenceDurations, in.Duration) {
		return 0, "", refuse(http.StatusBadRequest, model.ErrCodeInvalidInput,
			"duration must be one of %s", strings.Join(c.SilenceDurations, ", "))
	}
	for _, m := range in.Matchers {
		if m.Name == "alertname" && slices.Contains(c.ProtectedAlerts, m.Value) {
			return 0, "", refuse(http.StatusForbidden, model.ErrCodeForbidden,
				"%s is a protected alert and cannot be silenced through dash", m.Value)
		}
	}
	return dur, strings.TrimSpace(in.Comment), nil
}

// checkExpire is the rule for expiring: the silence must exist, and dash must have created it.
func checkExpire(silences []model.Silence, id string) *plugin.ActionError {
	i := slices.IndexFunc(silences, func(s model.Silence) bool { return s.ID == id })
	if i < 0 {
		return refuse(http.StatusNotFound, model.ErrCodeNotFound, "no such silence")
	}
	if !strings.HasPrefix(silences[i].CreatedBy, dashCreator) {
		return refuse(http.StatusForbidden, model.ErrCodeForbidden, "dash can expire only the silences it created")
	}
	return nil
}

// Act runs silence.create and silence.expire. A silence is created as "dash:<user>", and only
// such silences can be expired: expire reads Alertmanager's list first, so with Alertmanager
// down it fails (502) instead of expiring blind.
func (p *Plugin) Act(ctx context.Context, action string, pr auth.Principal, input any) (any, error) {
	switch in := input.(type) {
	case *silenceCreateInput:
		return p.createSilence(ctx, pr, in)
	case *silenceExpireInput:
		return p.expireSilence(ctx, in)
	}
	return nil, refuse(http.StatusNotFound, model.ErrCodeNotFound, "no such action: %s", action)
}

func (p *Plugin) createSilence(ctx context.Context, pr auth.Principal, in *silenceCreateInput) (any, error) {
	c, am, now := p.runtime()
	dur, comment, refusal := planSilence(&c, in)
	if refusal != nil {
		return nil, refusal
	}
	id, err := am.CreateSilence(ctx, upstream.SilenceSpec{
		Matchers:  in.Matchers,
		StartsAt:  now,
		EndsAt:    now.Add(dur),
		CreatedBy: dashCreator + pr.User,
		Comment:   comment,
	})
	if err != nil {
		return nil, upstreamError(err)
	}
	return plugin.Outcome{
		Body:   model.SilenceResponse{SilenceID: id},
		Target: &plugin.ActionTarget{Kind: "Silence", Name: id},
	}, nil
}

func (p *Plugin) expireSilence(ctx context.Context, in *silenceExpireInput) (any, error) {
	_, am, _ := p.runtime()
	silences, err := am.Silences(ctx)
	if err != nil {
		return nil, upstreamError(err)
	}
	if refusal := checkExpire(silences, in.ID); refusal != nil {
		return nil, refusal
	}
	if err := am.ExpireSilence(ctx, in.ID); err != nil {
		return nil, upstreamError(err)
	}
	return nil, nil
}

// upstreamError turns what the client reports into what the action answers: its own input
// refusal is a 400, anything else goes up as it is and becomes a 502 naming only a category.
func upstreamError(err error) error {
	if errors.Is(err, upstream.ErrInvalid) {
		return refuse(http.StatusBadRequest, model.ErrCodeInvalidInput, "%s", err.Error())
	}
	return err
}
