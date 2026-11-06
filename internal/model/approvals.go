package model

import (
	"encoding/json"
	"time"
)

// States of an approval request (design O.2, O.3).
const (
	// ApprovalStatePending: the request waits for its owner's decision.
	ApprovalStatePending = "pending"
	// ApprovalStateApproved: the owner approved it and the action ran; Outcome says how it went.
	ApprovalStateApproved = "approved"
	// ApprovalStateDenied: the owner denied it, or the requester lost a permission the action
	// needs before it could run (Reason says which).
	ApprovalStateDenied = "denied"
	// ApprovalStateExpired: nobody decided within ApprovalExpiryMinutes.
	ApprovalStateExpired = "expired"
)

// ApprovalExpiryMinutes is how long a request waits for a decision. A decided or expired request
// stays readable for as long again, then dash forgets it; a restart forgets every request.
const ApprovalExpiryMinutes = 10

// ApprovalsPendingMax is how many pending requests one user may have; the next is refused with
// 429 rate_limited.
const ApprovalsPendingMax = 20

// ApprovalResultMaxBytes caps an executed action's stored result, after redaction.
const ApprovalResultMaxBytes = 64 << 10

// ApprovalStatusPending is the status of ApprovalPending.
const ApprovalStatusPending = "pending_approval"

// ApprovalTarget is what the action acts on, as its plugin names it.
type ApprovalTarget struct {
	Kind      string `json:"kind" dash:"required"`
	Namespace string `json:"namespace" dash:"required"` // "" for a target without one
	Name      string `json:"name" dash:"required"`
}

// ApprovalOutcome is how an approved action ran.
type ApprovalOutcome struct {
	OK bool `json:"ok" dash:"required"`
	// Result is the action's answer as JSON text, redacted and cut to ApprovalResultMaxBytes
	// (Truncated then says so); "" when it failed.
	Result    string `json:"result" dash:"required"`
	Truncated bool   `json:"truncated" dash:"required"`
	// Error is why the action failed, for a person; "" when it succeeded.
	Error string    `json:"error" dash:"required"`
	At    time.Time `json:"at" dash:"required"`
}

// ApprovalInfo is one approval request: an AI agent asked to run a plugin action, and its owner
// approves or denies it (design O.2). GET /api/v1/approvals/{id} shows it to its owner, the
// core.approvals topic lists the pending ones, and the MCP tool dash_approval answers it.
type ApprovalInfo struct {
	ID    string `json:"id" dash:"required"` // appr_ and 26 base32 characters
	State string `json:"state" dash:"required,enum=pending|approved|denied|expired"`
	User  string `json:"user" dash:"required"` // the owner: the requesting credential's user
	// TokenID or GrantID is the credential the agent called with: a personal access token or an
	// OAuth grant. CredentialName is its name, or the OAuth client's name for a grant.
	TokenID        string `json:"tokenId,omitempty"`
	GrantID        string `json:"grantId,omitempty"`
	CredentialName string `json:"credentialName" dash:"required"`
	// ClientName is the MCP client's own name (clientInfo), "" when it sent none.
	ClientName string `json:"clientName" dash:"required"`
	Plugin     string `json:"plugin" dash:"required"` // the plugin instance
	Action     string `json:"action" dash:"required"` // the action's name, e.g. restart
	Title      string `json:"title" dash:"required"`  // the action's title
	Tool       string `json:"tool" dash:"required"`   // the MCP tool, <plugin>_<action with . -> _>
	// Summary is the action's one-line description of this call.
	Summary string         `json:"summary" dash:"required"`
	Target  ApprovalTarget `json:"target" dash:"required"`
	// Input is the action's input as dash validated it, without confirm: what runs on approval.
	// null in core.approvals, which lists requests; GET /api/v1/approvals/{id} has it.
	Input json.RawMessage `json:"input" dash:"required" tstype:"unknown"`
	// InputSHA256 is the hex SHA-256 of Input: the audit records it instead of the values.
	InputSHA256 string `json:"inputSha256" dash:"required"`
	Destructive bool   `json:"destructive" dash:"required"`
	// Confirm is the target's confirmation rule; typeName means the decision must carry the
	// target's name in confirm.
	Confirm   Confirm   `json:"confirm" dash:"required,enum=none|dialog|typeName"`
	CreatedAt time.Time `json:"createdAt" dash:"required"`
	ExpiresAt time.Time `json:"expiresAt" dash:"required"`
	// DecidedAt and DecidedBy are when and by whom it was approved or denied; null and "" while
	// pending, after expiry, and when dash denied it (Reason).
	DecidedAt *time.Time `json:"decidedAt" dash:"required,nullable" tstype:"string | null,required"`
	DecidedBy string     `json:"decidedBy" dash:"required"`
	// Reason is why a request was denied without its owner: the requester lost a permission or
	// the action is no longer offered. "" otherwise.
	Reason string `json:"reason" dash:"required"`
	// Outcome is how the action ran, once approved; absent before.
	Outcome *ApprovalOutcome `json:"outcome,omitempty"`
}

// ApprovalDecision is the body of POST /api/v1/approvals/{id}/decision.
type ApprovalDecision struct {
	Approve bool `json:"approve" dash:"required"`
	// Confirm is the target's name, typed by the person approving; required to approve a
	// request whose rule is typeName, and ignored otherwise.
	Confirm string `json:"confirm,omitempty" dash:"max=253"`
}

// ApprovalsData is the payload of core.approvals: the caller's pending requests, oldest first,
// each without its input (null). The topic is per user: nobody sees another user's requests.
type ApprovalsData struct {
	Pending []ApprovalInfo `json:"pending" dash:"required"`
}

// ApprovalPending is an action tool's answer to an MCP client that cannot open a URL itself
// (no URL elicitation): the action waits for approval at ApproveURL, and the agent polls
// dash_approval with ApprovalID.
type ApprovalPending struct {
	Status     string    `json:"status" dash:"required,enum=pending_approval"`
	ApprovalID string    `json:"approvalId" dash:"required"`
	ApproveURL string    `json:"approveURL" dash:"required"`
	ExpiresAt  time.Time `json:"expiresAt" dash:"required"`
}
