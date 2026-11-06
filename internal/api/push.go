package api

import (
	"cmp"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/push"
)

// The audit actions of the push routes. A record names the subscription by its ID and the push
// service by its host (target namespace), never by the endpoint: anyone holding that URL can
// post to the device.
const (
	auditPushSubscribe   = "push.subscribe"
	auditPushVerify      = "push.verify"
	auditPushUnsubscribe = "push.unsubscribe"
	auditPushTest        = "push.test"
)

// pushTestWindow is how often one subscription may get a test push (contract D44).
const pushTestWindow = time.Minute

// pushRegisterLimit is how many registrations (new or again) one user makes in
// pushRegisterWindow: each may send a verify push.
const (
	pushRegisterLimit  = 10
	pushRegisterWindow = time.Minute
)

// pushRoutes are the push device routes (design P.2, contract D45): the scope push, so the
// dash-android grant reaches them as well as a person; viewer role; never the anonymous viewer
// or a static token. Writes are CSRF-checked for cookies and audited fail-closed.
func (s *Server) pushRoutes() []Route {
	row := func(r Route) Route {
		r.Role, r.Scope, r.Tag = auth.RoleViewer, model.OAuthScopePush, "Push"
		return r
	}
	unavailable := " 409 push_unavailable when push is off (it needs a writable data dir and DASH_SECRET_KEY in auth.mode local, oidc or proxy)."
	id := []plugin.Param{{Name: "id", Doc: "The subscription's ID, sub_<26 base32>.", Required: true}}
	return []Route{
		row(Route{
			ID: "getPushVAPID", Method: "GET", Path: "/api/v1/push/vapid",
			Summary:     "The VAPID public key and the server URL",
			Description: "The applicationServerKey a browser subscribes with, made at the first request, and the URL a native client connects to." + unavailable,
			Response:    model.PushVAPID{}, Handler: s.pushVAPID,
		}),
		row(Route{
			ID: "listPushSubscriptions", Method: "GET", Path: "/api/v1/push/subscriptions",
			Summary: "List push devices",
			Description: "The caller's own devices; an administrator may list one user's (?user=<name>) or everyone's (?user=*). " +
				"Never the endpoints or the keys. 403 for another user's list to a viewer." + unavailable,
			Query:    []plugin.Param{{Name: "user", Doc: "Administrators only: a user name, or * for every user. Absent or empty: the caller's own."}},
			Response: model.PushSubscriptionList{}, Handler: s.listPushSubscriptions,
		}),
		row(Route{
			ID: "createPushSubscription", Method: "POST", Path: "/api/v1/push/subscriptions",
			Summary: "Register a push device",
			Description: "201 with a new subscription, which receives a verify push with its code and nothing else until " +
				"POST .../verify echoes it; it is deleted after 10 minutes unverified. 200 when the caller already holds the endpoint: " +
				"the row is replaced, verified still when the keys are the same. 400 invalid_input with field errors; 409 conflict when " +
				"another user holds the endpoint; 409 push_limit past 10 devices; 429 past 10 registrations a minute; 503 audit_unavailable." + unavailable,
			Request: model.PushSubscriptionRequest{}, Response: model.PushSubscriptionInfo{}, Status: http.StatusCreated, Handler: s.createPushSubscription,
		}),
		row(Route{
			ID: "verifyPushSubscription", Method: "POST", Path: "/api/v1/push/subscriptions/{id}/verify",
			Summary: "Verify a push device with its code",
			Description: "422 invalid_input for a wrong code; the fifth wrong code deletes the subscription. 409 conflict when it is not " +
				"pending; 404 for another user's." + unavailable,
			PathParams: id, Request: model.PushVerifyRequest{}, Response: model.PushSubscriptionInfo{}, Handler: s.verifyPushSubscription,
		}),
		row(Route{
			ID: "testPushSubscription", Method: "POST", Path: "/api/v1/push/subscriptions/{id}/test",
			Summary:     "Send a test push",
			Description: "202: queued. 409 conflict when the subscription is not verified; 429 past one a minute per subscription." + unavailable,
			PathParams:  id, Status: http.StatusAccepted, Handler: s.testPushSubscription,
		}),
		row(Route{
			ID: "deletePushSubscription", Method: "DELETE", Path: "/api/v1/push/subscriptions/{id}",
			Summary: "Remove a push device", Description: "One's own device, or (administrators) anyone's. 404 for a device that is not the caller's." + unavailable,
			PathParams: id, Status: http.StatusNoContent, Handler: s.deletePushSubscription,
		}),
	}
}

// pushReady answers 409 push_unavailable when push is off (contract D37).
func (s *Server) pushReady(w http.ResponseWriter) bool {
	if s.d.Push == nil {
		writeErr(w, http.StatusConflict, model.ErrCodePushUnavailable, "push is off: it needs a writable data dir and DASH_SECRET_KEY in auth.mode local, oidc or proxy")
		return false
	}
	return true
}

// pushOwned is the subscription id of the caller; a missing one and another user's are the
// same 404. adminToo also gives an administrator anyone's.
func (s *Server) pushOwned(w http.ResponseWriter, r *http.Request, adminToo bool) (credentials.PushSubscription, bool) {
	p := principalFrom(r.Context())
	sub, ok := s.d.Credentials.PushSubscriptionByID(r.PathValue("id"))
	if !ok || sub.User != p.User && !(adminToo && p.Role.AtLeast(auth.RoleAdmin)) {
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "no such push subscription")
		return credentials.PushSubscription{}, false
	}
	return sub, true
}

func pushInfo(p credentials.PushSubscription) model.PushSubscriptionInfo {
	return model.PushSubscriptionInfo{
		ID: p.ID, User: p.User, Kind: p.Kind, DeviceName: p.DeviceName, State: p.State, EndpointHost: p.EndpointHost,
		Notify:    model.PushNotify{Approvals: p.Approvals, Attention: p.Attention},
		CreatedAt: p.CreatedAt, VerifiedAt: p.VerifiedAt,
	}
}

func pushTarget(sub credentials.PushSubscription) audit.Target {
	return audit.Target{Kind: "pushSubscription", Namespace: sub.EndpointHost, Name: sub.ID}
}

func (s *Server) pushVAPID(w http.ResponseWriter, _ *http.Request) {
	if !s.pushReady(w) {
		return
	}
	key, err := s.d.Push.VAPIDPublicKey()
	if err != nil {
		slog.Error("push: the VAPID key could not be made", "error", err)
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "the VAPID key could not be made")
		return
	}
	writeJSON(w, http.StatusOK, model.PushVAPID{PublicKey: key, ServerURL: cmp.Or(s.d.Cfg.DirectURL, s.d.Cfg.PublicURL)})
}

func (s *Server) listPushSubscriptions(w http.ResponseWriter, r *http.Request) {
	if !s.pushReady(w) {
		return
	}
	p := principalFrom(r.Context())
	user := p.User
	if u := strings.Join(r.URL.Query()["user"], ","); u != "" { // an empty ?user= is the caller's own
		if u != p.User && !p.Role.AtLeast(auth.RoleAdmin) {
			s.deny(r, obs.AuthForbidden)
			writeErr(w, http.StatusForbidden, model.ErrCodeForbidden, "only an administrator lists another user's push devices")
			return
		}
		user = u
		if u == "*" {
			user = "" // every user's
		}
	}
	list := model.PushSubscriptionList{Subscriptions: []model.PushSubscriptionInfo{}}
	for _, sub := range s.d.Credentials.PushSubscriptions(user) {
		list.Subscriptions = append(list.Subscriptions, pushInfo(sub))
	}
	writeJSON(w, http.StatusOK, list)
}

// pushRequestProblems checks a registration; no message repeats a value. host is the
// endpoint's host when the endpoint is usable.
func pushRequestProblems(req model.PushSubscriptionRequest) (host string, fields []model.SettingsFieldError) {
	add := func(path, msg string) { fields = append(fields, model.SettingsFieldError{Path: path, Message: msg}) }
	u, err := url.Parse(req.Endpoint)
	switch {
	case len(req.Endpoint) > 2048 || err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || strings.Contains(req.Endpoint, "#"):
		add("endpoint", "must be an https URL with a host, no user information and no fragment, at most 2048 bytes")
	default:
		host = strings.ToLower(u.Hostname())
	}
	if _, _, err := push.DecodeKeys(req.Keys.P256dh, req.Keys.Auth); err != nil {
		add("keys", "must be p256dh, an uncompressed P-256 public key, and auth, 16 bytes, both in base64url")
	}
	if req.Kind != model.PushKindWebPush && req.Kind != model.PushKindUnifiedPush {
		add("kind", "must be webpush or unifiedpush")
	}
	if !validTokenName(req.DeviceName) {
		add("deviceName", "must be 1 to 64 characters with at least one visible one, without control or format characters or surrounding spaces")
	}
	switch req.Notify.Attention {
	case model.PushAttentionCrit, model.PushAttentionWarn, model.PushAttentionOff:
	default:
		add("notify.attention", "must be crit, warn or off")
	}
	return host, fields
}

func (s *Server) createPushSubscription(w http.ResponseWriter, r *http.Request) {
	if !s.pushReady(w) {
		return
	}
	p := principalFrom(r.Context())
	var body model.PushSubscriptionRequest
	if !s.decodeStrict(w, r, &body) {
		return
	}
	host, fields := pushRequestProblems(body)
	if len(fields) > 0 {
		writeErrDetails(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "invalid push subscription", &model.SettingsErrorDetails{Fields: fields})
		return
	}
	// Every registration counts, a repeat included: each may push a code.
	if ok, retry := s.pushRegisterLimit.allow(p.User, s.now()); !ok {
		s.deny(r, obs.AuthRateLimit)
		w.Header().Set("Retry-After", retryAfterSeconds(retry))
		writeErr(w, http.StatusTooManyRequests, model.ErrCodeRateLimited, "too many push registrations; try again later")
		return
	}
	// The record names the row the request creates or replaces.
	id, err := credentials.NewPushID()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "internal error")
		return
	}
	replacing := false
	if prev, ok := s.d.Credentials.PushSubscriptionByEndpoint(body.Endpoint); ok {
		if prev.User != p.User {
			writePushTaken(w)
			return
		}
		id, replacing = prev.ID, true
	}
	code, err := credentials.NewPushCode()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "internal error")
		return
	}
	pending, ok := s.beginRecord(w, s.auditRecord(r, auditPushSubscribe, audit.Target{Kind: "pushSubscription", Namespace: host, Name: id}))
	if !ok {
		return
	}
	sub, sendCode, err := s.d.Credentials.AddPushSubscription(credentials.PushInput{
		ID: id, User: p.User, Kind: body.Kind, DeviceName: body.DeviceName, Approvals: body.Notify.Approvals, Attention: body.Notify.Attention,
		Endpoint: body.Endpoint, EndpointHost: host, P256dh: body.Keys.P256dh, Auth: body.Keys.Auth,
	}, code, s.now())
	pending.Outcome(err)
	switch {
	case errors.Is(err, credentials.ErrPushEndpointTaken):
		writePushTaken(w)
		return
	case errors.Is(err, credentials.ErrTooManyPushSubscriptions):
		writeErr(w, http.StatusConflict, model.ErrCodePushLimit, "you hold the most push devices allowed (10); remove one first")
		return
	case err != nil:
		slog.Error("push subscribe: the credential store refused the write", "error", err, "requestId", requestIDFrom(r.Context()))
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "the subscription could not be stored")
		return
	}
	if sendCode != "" {
		s.d.Push.SendVerify(sub.ID, sendCode)
	}
	status := http.StatusCreated
	if replacing {
		status = http.StatusOK
	}
	writeJSON(w, status, pushInfo(sub))
}

func writePushTaken(w http.ResponseWriter) {
	writeErr(w, http.StatusConflict, model.ErrCodeConflict, "another user registered this push endpoint")
}

func (s *Server) verifyPushSubscription(w http.ResponseWriter, r *http.Request) {
	if !s.pushReady(w) {
		return
	}
	sub, ok := s.pushOwned(w, r, false)
	if !ok {
		return
	}
	var body model.PushVerifyRequest
	if !s.decodeStrict(w, r, &body) {
		return
	}
	pending, ok := s.beginRecord(w, s.auditRecord(r, auditPushVerify, pushTarget(sub)))
	if !ok {
		return
	}
	verified, err := s.d.Credentials.VerifyPushSubscription(sub.User, sub.ID, body.Code, s.now())
	pending.Outcome(err)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, pushInfo(verified))
	case errors.Is(err, credentials.ErrPushWrongCode):
		writeErr(w, http.StatusUnprocessableEntity, model.ErrCodeInvalidInput, "wrong code")
	case errors.Is(err, credentials.ErrPushCodeExhausted):
		writeErr(w, http.StatusUnprocessableEntity, model.ErrCodeInvalidInput, "wrong code, the fifth: the subscription was deleted; subscribe again")
	case errors.Is(err, credentials.ErrPushNotPending):
		writeErr(w, http.StatusConflict, model.ErrCodeConflict, "the subscription is not waiting for a code")
	case errors.Is(err, credentials.ErrPushNotFound):
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "no such push subscription")
	default:
		slog.Error("push verify: the credential store refused the write", "error", err, "requestId", requestIDFrom(r.Context()))
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "the subscription could not be stored")
	}
}

func (s *Server) testPushSubscription(w http.ResponseWriter, r *http.Request) {
	if !s.pushReady(w) {
		return
	}
	sub, ok := s.pushOwned(w, r, false)
	if !ok {
		return
	}
	if sub.State != credentials.PushVerified {
		writeErr(w, http.StatusConflict, model.ErrCodeConflict, "the subscription is not verified")
		return
	}
	if ok, retry := s.pushTestLimit.allow(sub.ID, s.now()); !ok {
		w.Header().Set("Retry-After", retryAfterSeconds(retry))
		writeErr(w, http.StatusTooManyRequests, model.ErrCodeRateLimited, "one test push a minute per device")
		return
	}
	pending, ok := s.beginRecord(w, s.auditRecord(r, auditPushTest, pushTarget(sub)))
	if !ok {
		return
	}
	err := s.d.Push.SendTest(sub.ID)
	pending.Outcome(err)
	if err != nil {
		writeErr(w, http.StatusConflict, model.ErrCodeConflict, "the subscription is not verified")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) deletePushSubscription(w http.ResponseWriter, r *http.Request) {
	if !s.pushReady(w) {
		return
	}
	sub, ok := s.pushOwned(w, r, true)
	if !ok {
		return
	}
	pending, ok := s.beginRecord(w, s.auditRecord(r, auditPushUnsubscribe, pushTarget(sub)))
	if !ok {
		return
	}
	_, _, err := s.d.Credentials.DeletePushSubscription(sub.ID)
	pending.Outcome(err)
	if err != nil {
		slog.Error("push unsubscribe: credentials.yaml could not be written; the device is removed until dash restarts", "error", err, "requestId", requestIDFrom(r.Context()))
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "the device is removed until dash restarts: credentials.yaml could not be written")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// retryAfterSeconds is a Retry-After value: whole seconds, rounded up.
func retryAfterSeconds(d time.Duration) string { return strconv.Itoa(int(math.Ceil(d.Seconds()))) }
