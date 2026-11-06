package model

import "time"

// Kinds of push subscription (design P.2). The payload is the same for both; the kind labels
// the device in Settings -> Devices.
const (
	PushKindWebPush     = "webpush"     // a browser's PushManager subscription (the PWA)
	PushKindUnifiedPush = "unifiedpush" // a UnifiedPush distributor's endpoint (the Android app)
)

// Attention thresholds of a subscription (design P.3): crit pushes critical items only, warn
// pushes warnings and critical items, off pushes none.
const (
	PushAttentionCrit = "crit"
	PushAttentionWarn = "warn"
	PushAttentionOff  = "off"
)

// States of a subscription (contract D39, D41).
const (
	// PushStatePending: registered, the verification code was pushed, and nothing else is sent
	// until the code comes back. Deleted 10 minutes after it was registered.
	PushStatePending = "pending"
	// PushStateVerified: the code came back; the subscription receives pushes.
	PushStateVerified = "verified"
	// PushStateStale: a web-push subscription bound to a VAPID key dash no longer holds (the
	// secret key was rotated without DASH_SECRET_KEY_PREVIOUS). It receives nothing; the device
	// subscribes again.
	PushStateStale = "stale"
)

// Types of push message (design P.2, contract D41, D44).
const (
	PushTypeApproval  = "approval"  // an action waits for a person's decision
	PushTypeAttention = "attention" // an attention item lasted 2 minutes, or the hourly summary
	PushTypeVerify    = "verify"    // the verification code, the only push before verification
	PushTypeTest      = "test"      // Settings -> Devices -> Send test push
)

// PushCodeLen is the length of a verification code: Crockford base32 characters.
const PushCodeLen = 8

// PushDeviceNameMaxLen is the longest device name, in characters.
const PushDeviceNameMaxLen = 64

// PushSubscriptionsMax is how many subscriptions one user may hold (contract D40).
const PushSubscriptionsMax = 10

// PushVAPID is GET /api/v1/push/vapid: the applicationServerKey a browser subscribes with.
type PushVAPID struct {
	// PublicKey is the VAPID public key, an uncompressed P-256 point (65 bytes) in unpadded
	// base64url, as PushManager.subscribe takes it.
	PublicKey string `json:"publicKey" dash:"required"`
	// ServerURL is the address the Android app connects to, encoded in Settings -> Devices' QR
	// code: server.directURL when set, otherwise server.publicURL.
	ServerURL string `json:"serverUrl" dash:"required"`
}

// PushKeys are the subscription's keys (RFC 8291), as PushSubscription.toJSON() gives them.
type PushKeys struct {
	// P256dh is the device's P-256 public key, uncompressed (65 bytes), in unpadded base64url.
	P256dh string `json:"p256dh" dash:"required,min=1,max=128"`
	// Auth is the 16-byte authentication secret in unpadded base64url.
	Auth string `json:"auth" dash:"required,min=1,max=64"`
}

// PushNotify is what a subscription receives besides verification and test pushes.
type PushNotify struct {
	// Approvals pushes every approval request of the subscription's user.
	Approvals bool `json:"approvals" dash:"required"`
	// Attention is the lowest severity of attention item pushed: crit, warn or off.
	Attention string `json:"attention" dash:"required,enum=crit|warn|off"`
}

// PushSubscriptionRequest is the body of POST /api/v1/push/subscriptions. Registering an
// endpoint the user already holds replaces that subscription (same ID); it stays verified when
// the keys are unchanged, and is verified again otherwise. An endpoint another user holds is
// refused (409).
type PushSubscriptionRequest struct {
	// Endpoint is the push service URL the subscription posts to: https, at most 2048 bytes.
	// dash never shows, logs or audits it: anyone holding it can post to the device.
	Endpoint string   `json:"endpoint" dash:"required,min=1,max=2048"`
	Keys     PushKeys `json:"keys" dash:"required"`
	Kind     string   `json:"kind" dash:"required,enum=webpush|unifiedpush"`
	// DeviceName is 1 to 64 characters, chosen by the user ("Pixel 9", "Firefox on the laptop").
	DeviceName string     `json:"deviceName" dash:"required,min=1,max=64"`
	Notify     PushNotify `json:"notify" dash:"required"`
}

// PushSubscriptionInfo is one subscription as the API shows it: never the endpoint or the keys.
type PushSubscriptionInfo struct {
	ID         string     `json:"id" dash:"required"`   // sub_<26 base32>
	User       string     `json:"user" dash:"required"` // the owner
	Kind       string     `json:"kind" dash:"required,enum=webpush|unifiedpush"`
	DeviceName string     `json:"deviceName" dash:"required"`
	Notify     PushNotify `json:"notify" dash:"required"`
	State      string     `json:"state" dash:"required,enum=pending|verified|stale"`
	// EndpointHost is the push service's host name (fcm.googleapis.com, ntfy.example.com).
	EndpointHost string    `json:"endpointHost" dash:"required"`
	CreatedAt    time.Time `json:"createdAt" dash:"required"`
	// VerifiedAt is when the code came back; null while pending.
	VerifiedAt *time.Time `json:"verifiedAt" dash:"required,nullable" tstype:"string | null,required"`
}

// PushSubscriptionList is GET /api/v1/push/subscriptions: the caller's subscriptions, or
// (administrators) one user's or everyone's.
type PushSubscriptionList struct {
	Subscriptions []PushSubscriptionInfo `json:"subscriptions" dash:"required"`
}

// PushVerifyRequest is the body of POST /api/v1/push/subscriptions/{id}/verify. Five wrong
// codes delete the subscription.
type PushVerifyRequest struct {
	// Code is the 8 Crockford base32 characters of the verify push; case, I, L, O and U are
	// read as Crockford does, and hyphens and spaces are ignored.
	Code string `json:"code" dash:"required,min=1,max=32"`
}

// PushMessage is the JSON a push carries, encrypted end to end (RFC 8291), at most 3 KiB. It
// holds lab text (titles, pod names) and never a secret; a tap opens URL.
type PushMessage struct {
	Type string `json:"type" dash:"required,enum=approval|attention|verify|test"`
	// ID names what the push is about (the approval or attention item); the notification's tag.
	ID    string `json:"id" dash:"required"`
	Title string `json:"title" dash:"required"`
	Body  string `json:"body" dash:"required"`
	// URL is the absolute URL a tap opens: dash's public URL and a path, or the public URL itself
	// when there is nothing in particular to open.
	URL string    `json:"url" dash:"required"`
	TS  time.Time `json:"ts" dash:"required"`
	// Code is the verification code of a verify push; absent from every other type.
	Code string `json:"code,omitempty"`
}
