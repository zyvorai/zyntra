// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package approvals keeps decision records: what Zyntra proposed and why,
// who approved it, whether it still held when it ran, what ran and what
// happened afterwards. Proposals move through pending -> approved|rejected|
// expired -> executed|failed|blocked, with a hash-chained audit trail
// persisted as JSON.
package approvals

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/zyvorai/zyntra/internal/adapters"
	"github.com/zyvorai/zyntra/internal/executor"
	"github.com/zyvorai/zyntra/internal/freshness"
	"github.com/zyvorai/zyntra/internal/ontology"
	"github.com/zyvorai/zyntra/internal/outcome"
	"github.com/zyvorai/zyntra/internal/policy"
	"github.com/zyvorai/zyntra/internal/sim"
)

type Status string

const (
	Pending  Status = "pending"
	Approved Status = "approved"
	Rejected Status = "rejected"
	Expired  Status = "expired"
	Blocked  Status = "blocked"
	Executed Status = "executed"
	Failed   Status = "failed"
)

// Phase is where a decision stands, beyond its approval status.
type Phase string

const (
	PhaseProposed         Phase = "proposed"
	PhaseApproved         Phase = "approved"
	PhaseRejected         Phase = "rejected"
	PhaseExpired          Phase = "expired"
	PhaseBlocked          Phase = "blocked"
	PhaseFailed           Phase = "failed"
	PhaseDryRunValidated  Phase = "dry-run-validated"
	PhaseApplied          Phase = "applied"
	PhaseObserving        Phase = "observing"
	PhaseVerified         Phase = "verified"
	PhaseRegressed        Phase = "regressed"
	PhaseMissed           Phase = "missed"
	PhaseInconclusive     Phase = "inconclusive"
	PhaseRollbackProposed Phase = "rollback-proposed"
	PhaseRolledBack       Phase = "rolled-back"
)

const schemaVersion = 2

var (
	ErrNotFound = errors.New("proposal not found")
	// ErrForbidden marks approvals refused by policy (e.g. the two-person
	// rule).
	ErrForbidden = errors.New("not allowed")
)

// KeepRef links a proposal to Fabric Keep's records.
type KeepRef struct {
	Mode       string `json:"mode"` // keep | mirror
	SessionID  string `json:"session_id,omitempty"`
	ApprovalID string `json:"approval_id,omitempty"`
	ReceiptID  string `json:"receipt_id,omitempty"`
	Error      string `json:"error,omitempty"`
}

type Prediction struct {
	SeverityBefore float64            `json:"severity_before"`
	SeverityAfter  float64            `json:"severity_after"`
	Closes         []string           `json:"closes,omitempty"`
	Opens          []string           `json:"opens,omitempty"`
	KPIs           map[string]float64 `json:"kpis"`
}

// Inputs is the evidence a decision was made on.
type Inputs struct {
	At        time.Time          `json:"at"`
	Values    map[string]float64 `json:"values"`
	Freshness []freshness.State  `json:"freshness,omitempty"`
	Sources   []adapters.Status  `json:"sources,omitempty"`
}

// Alternative is another candidate the planner considered.
type Alternative struct {
	Action              string   `json:"action"`
	Name                string   `json:"name"`
	Score               float64  `json:"score"`
	WeightedImprovement float64  `json:"weighted_improvement"`
	Confidence          string   `json:"confidence,omitempty"`
	BlockedReasons      []string `json:"blocked_reasons,omitempty"`
}

// Approval is one approver's sign-off.
type Approval struct {
	By     string    `json:"by"`
	Role   string    `json:"role,omitempty"`
	Method string    `json:"method,omitempty"`
	At     time.Time `json:"at"`
	Reason string    `json:"reason,omitempty"`
}

// Revalidation is the pre-execution check.
type Revalidation struct {
	At                  time.Time `json:"at"`
	OK                  bool      `json:"ok"`
	Reasons             []string  `json:"reasons,omitempty"`
	ModelVersion        string    `json:"model_version,omitempty"`
	WeightedImprovement float64   `json:"weighted_improvement"`
	Drift               float64   `json:"drift"`
	StaleInputs         []string  `json:"stale_inputs,omitempty"`
}

type Proposal struct {
	ID         string   `json:"id"`
	Action     string   `json:"action"`
	Actions    []string `json:"actions,omitempty"`
	ActionName string   `json:"action_name"`
	Risk       string   `json:"risk,omitempty"`
	Adapter    string   `json:"adapter,omitempty"`
	Template   string   `json:"template,omitempty"`
	Render     string   `json:"render,omitempty"`
	RenderErr  string   `json:"render_error,omitempty"`
	// Kinds are what running the proposal does (kubectl, webhook, file,
	// noop), one per executable action.
	Kinds []string `json:"kinds,omitempty"`
	// Compensate lists the actions that undo this one; they are linked, not
	// run automatically.
	Compensate []string `json:"compensate,omitempty"`
	// Evidence holds, per action, the source rows and fills its payload
	// was built from; execution renders from it.
	Evidence map[string]*executor.Evidence `json:"evidence,omitempty"`

	PackID       string             `json:"pack,omitempty"`
	ModelVersion string             `json:"model_version,omitempty"`
	Inputs       *Inputs            `json:"inputs,omitempty"`
	Simulation   *sim.Result        `json:"simulation,omitempty"`
	Alternatives []Alternative      `json:"alternatives,omitempty"`
	Policy       *policy.Effective  `json:"policy,omitempty"`
	Predicted    Prediction         `json:"predicted"`
	Baseline     map[string]float64 `json:"baseline"`
	Actual       map[string]float64 `json:"actual,omitempty"`

	Status    Status     `json:"status"`
	Phase     Phase      `json:"phase"`
	CreatedAt time.Time  `json:"created_at"`
	CreatedBy string     `json:"created_by"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Approvals []Approval `json:"approvals"`
	// RequiredApprovals is the quorum; 0 on records older than v0.3.
	RequiredApprovals int        `json:"required_approvals"`
	DecidedAt         *time.Time `json:"decided_at,omitempty"`
	DecidedBy         string     `json:"decided_by,omitempty"`
	Reason            string     `json:"reason,omitempty"`

	// WaitingForWindow is set on approved proposals held until their
	// maintenance window opens.
	WaitingForWindow bool             `json:"waiting_for_window,omitempty"`
	Revalidation     *Revalidation    `json:"revalidation,omitempty"`
	BlockedReasons   []string         `json:"blocked_reasons,omitempty"`
	Execution        *executor.Result `json:"execution,omitempty"`
	ExecutedAt       *time.Time       `json:"executed_at,omitempty"`
	Outcome          *outcome.Record  `json:"outcome,omitempty"`
	// Tenant is the tenant whose objects this proposal acts on ("" for a
	// deployment-wide proposal). Tenant-bound identities only ever see and
	// decide proposals of their own tenant.
	Tenant string `json:"tenant,omitempty"`
	// Objects and ActionInputs record what a typed action works on.
	Objects      []ontology.ObjectRef `json:"objects,omitempty"`
	ActionInputs map[string]string    `json:"action_inputs,omitempty"`
	// ObjectDigest fingerprints the contract and facts the approval was
	// given for; the executor refuses to run if they have changed.
	ObjectDigest string `json:"object_digest,omitempty"`
	// ScenarioID links the proposal to the saved scenario it came from.
	ScenarioID string `json:"scenario_id,omitempty"`
	// Rollout is the staged-delivery shape the deployment tooling should
	// follow; it is part of the signed decision record.
	Rollout *ontology.Rollout `json:"rollout,omitempty"`
	// ObjectOutcome is the object-level verdict, set when the outcome is
	// decided: business objects still failing a bound KPI.
	ObjectOutcome *ObjectOutcome `json:"object_outcome,omitempty"`
	// Explanation says why the outcome matched the prediction or not.
	Explanation *outcome.Explanation `json:"explanation,omitempty"`
	Keep        *KeepRef             `json:"keep,omitempty"`

	// RollbackOf links a rollback proposal to the decision it undoes;
	// RollbackID links the other way.
	RollbackOf string `json:"rollback_of,omitempty"`
	RollbackID string `json:"rollback_id,omitempty"`
}

// Event is one audit entry. Hash covers the entry and PrevHash, so editing
// or removing any entry breaks the chain from there on.
type Event struct {
	Seq      int       `json:"seq"`
	At       time.Time `json:"at"`
	Proposal string    `json:"proposal"`
	Action   string    `json:"action"`
	From     Status    `json:"from,omitempty"`
	To       Status    `json:"to"`
	Phase    Phase     `json:"phase,omitempty"`
	By       string    `json:"by"`
	Note     string    `json:"note,omitempty"`
	// Payload is the SHA-256 of the rendered change the event refers to
	// (what was approved, or what was sent); Response is the SHA-256 of a
	// webhook's response body. Both are covered by Hash.
	Payload  string `json:"payload_sha256,omitempty"`
	Response string `json:"response_sha256,omitempty"`
	// Explanation is the SHA-256 of the miss explanation stored next to an
	// outcome verdict.
	Explanation string `json:"explanation_sha256,omitempty"`
	PrevHash    string `json:"prev_hash"`
	Hash        string `json:"hash"`
}

func (e Event) digest() string {
	e.Hash = ""
	b, _ := json.Marshal(e)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Verification is the result of checking the audit chain.
type Verification struct {
	OK       bool   `json:"ok"`
	Events   int    `json:"events"`
	Head     string `json:"head"`
	BrokenAt int    `json:"broken_at,omitempty"`
	Error    string `json:"error,omitempty"`
	Migrated bool   `json:"migrated,omitempty"`
}

type state struct {
	Version   int         `json:"version"`
	Migrated  *time.Time  `json:"migrated_at,omitempty"`
	Proposals []*Proposal `json:"proposals"`
	// Audit is held here in memory. On disk it lives in the append-only file
	// next to the state (see auditPath); only a legacy single-file state
	// carries it inline, and it is moved out on the next save.
	Audit []Event `json:"audit,omitempty"`
	// AuditFile marks a state whose audit trail is in the append-only file.
	AuditFile bool `json:"audit_file,omitempty"`
}

type Store struct {
	mu   sync.Mutex
	path string
	s    state
	now  func() time.Time
	// flushed is how many audit events are already in the audit file.
	flushed int
	onEvent func(Event)
}

// auditPath is the append-only audit file: one JSON event per line, never
// rewritten, so a save costs one line instead of the whole history.
func (s *Store) auditPath() string { return s.path + ".audit.jsonl" }

// Open loads path (empty path keeps everything in memory). State written
// by older versions is migrated: phases and approvals are derived from the
// old status fields and the audit trail is chained from its first entry.
func Open(path string) (*Store, error) {
	st := &Store{path: path, now: time.Now, s: state{Version: schemaVersion}}
	if path == "" {
		return st, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	st.s = state{}
	if err := json.Unmarshal(b, &st.s); err != nil {
		return nil, fmt.Errorf("approvals state %s: %w", path, err)
	}
	legacy := len(st.s.Audit) // events kept inline by an older version
	events, good, torn, err := readAuditFile(st.auditPath())
	if err != nil {
		return nil, err
	}
	if torn {
		// Cut the unfinished line so the next event starts on a fresh one.
		if err := os.Truncate(st.auditPath(), good); err != nil {
			return nil, err
		}
	}
	switch {
	case len(events) > 0 && legacy > 0:
		return nil, fmt.Errorf("approvals state %s holds %d inline audit events and %s holds %d: refusing to guess which is the real trail",
			path, legacy, st.auditPath(), len(events))
	case len(events) > 0:
		st.s.Audit, st.flushed = events, len(events)
	}
	if st.s.Version < schemaVersion {
		st.migrate()
	}
	// A legacy state is moved to the append-only file; the chain is intact.
	if st.s.Version < schemaVersion || legacy > 0 || !st.s.AuditFile {
		if err := st.save(); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// readAuditFile reads the append-only trail. A torn final line (a crash in
// the middle of an append) is ignored; a bad line anywhere else is an error,
// because it means the trail was altered.
func readAuditFile(path string) (events []Event, good int64, torn bool, err error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	var off int64
	for n := 1; ; n++ {
		line, rerr := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var e Event
			if err := json.Unmarshal(line, &e); err != nil {
				if rerr != nil { // the last line, and it did not finish: a torn append
					return events, good, true, nil
				}
				return nil, 0, false, fmt.Errorf("audit file %s line %d is damaged: %w", path, n, err)
			}
			if rerr != nil {
				// A complete event whose newline never landed: keep it, finish the line.
				events = append(events, e)
				return events, off + int64(len(line)), false, appendNewline(path)
			}
			events = append(events, e)
		}
		off += int64(len(line))
		good = off
		if rerr != nil {
			return events, good, false, nil
		}
	}
}

func appendNewline(path string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, werr := f.WriteString("\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// appendAudit writes the events not yet on disk and syncs them.
func (s *Store) appendAudit() error {
	pending := s.s.Audit[s.flushed:]
	if len(pending) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(s.auditPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, e := range pending {
		b, err := json.Marshal(e)
		if err != nil {
			f.Close()
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	s.flushed = len(s.s.Audit)
	return nil
}

func (s *Store) migrate() {
	for _, p := range s.s.Proposals {
		if p.Phase == "" {
			p.Phase = phaseFor(p.Status, p.Execution)
		}
		if p.Approvals == nil {
			p.Approvals = []Approval{}
			if p.DecidedBy != "" && p.Status != Rejected && p.Status != Pending && p.DecidedAt != nil {
				p.Approvals = append(p.Approvals, Approval{By: p.DecidedBy, Role: "admin", At: *p.DecidedAt, Reason: p.Reason})
			}
		}
	}
	prev := ""
	for i := range s.s.Audit {
		e := &s.s.Audit[i]
		e.Seq, e.PrevHash = i+1, prev
		e.Hash = e.digest()
		prev = e.Hash
	}
	now := s.now().UTC()
	s.s.Version, s.s.Migrated = schemaVersion, &now
}

func phaseFor(st Status, ex *executor.Result) Phase {
	switch st {
	case Approved:
		return PhaseApproved
	case Rejected:
		return PhaseRejected
	case Expired:
		return PhaseExpired
	case Blocked:
		return PhaseBlocked
	case Failed:
		return PhaseFailed
	case Executed:
		if ex != nil && ex.Mode == executor.ModeApply {
			return PhaseApplied
		}
		return PhaseDryRunValidated
	}
	return PhaseProposed
}

func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	// The audit trail goes first and is only ever appended to; if the state
	// write below then fails, the trail is ahead of the state, never behind.
	if err := s.appendAudit(); err != nil {
		return err
	}
	disk := s.s
	disk.Audit, disk.AuditFile = nil, true
	b, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "prop-" + hex.EncodeToString(b)
}

func (s *Store) audit(p *Proposal, from, to Status, by, note string) {
	s.auditHashes(p, from, to, by, note, renderHash(p.Render), "")
}

func renderHash(render string) string {
	if render == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(render))
	return hex.EncodeToString(sum[:])
}

func (s *Store) auditHashes(p *Proposal, from, to Status, by, note, payload, response string) {
	e := Event{Seq: len(s.s.Audit) + 1, At: s.now().UTC(), Proposal: p.ID, Action: p.Action, From: from, To: to, Phase: p.Phase, By: by, Note: note,
		Payload: payload, Response: response}
	if n := len(s.s.Audit); n > 0 {
		e.PrevHash = s.s.Audit[n-1].Hash
	}
	e.Hash = e.digest()
	s.s.Audit = append(s.s.Audit, e)
	if s.onEvent != nil {
		s.onEvent(e)
	}
}

// OnEvent registers a function called with every audit event, under the
// store's lock: it must return at once and must not call back into the store.
// Set it once at start-up.
func (s *Store) OnEvent(f func(Event)) {
	s.mu.Lock()
	s.onEvent = f
	s.mu.Unlock()
}

// Note appends an audit entry that is not tied to a proposal, such as an
// operator entering a manual KPI value. subject names what changed.
func (s *Store) Note(subject, by, note string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit(&Proposal{Action: subject}, "", "recorded", by, note)
	return s.save()
}

func clone(p *Proposal) Proposal {
	b, _ := json.Marshal(p)
	var c Proposal
	_ = json.Unmarshal(b, &c)
	return c
}

// Create adds a pending proposal. If one is already pending for the same
// action it is returned instead.
func (s *Store) Create(p Proposal, by string) (Proposal, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.s.Proposals {
		if x.Action == p.Action && x.Status == Pending && maps.Equal(x.ActionInputs, p.ActionInputs) {
			return clone(x), false, nil
		}
	}
	p.ID = newID()
	p.Status, p.Phase = Pending, PhaseProposed
	p.CreatedAt = s.now().UTC()
	p.CreatedBy = by
	if p.Approvals == nil {
		p.Approvals = []Approval{}
	}
	if p.Policy != nil && p.Policy.PendingExpiry > 0 && p.ExpiresAt == nil {
		exp := p.CreatedAt.Add(p.Policy.PendingExpiry)
		p.ExpiresAt = &exp
	}
	np := p
	s.s.Proposals = append(s.s.Proposals, &np)
	note := "proposed"
	if np.RollbackOf != "" {
		note = "rollback proposed for " + np.RollbackOf
	}
	s.audit(&np, "", Pending, by, note)
	return clone(&np), true, s.save()
}

func (s *Store) find(id string) (*Proposal, error) {
	for _, p := range s.s.Proposals {
		if p.ID == id {
			return p, nil
		}
	}
	return nil, ErrNotFound
}

func (s *Store) Get(id string) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.find(id)
	if err != nil {
		return Proposal{}, err
	}
	return clone(p), nil
}

// List returns proposals newest first.
func (s *Store) List() []Proposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Proposal, 0, len(s.s.Proposals))
	for _, p := range s.s.Proposals {
		out = append(out, clone(p))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (s *Store) Audit() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.s.Audit...)
}

// AuditFor returns the events of one proposal.
func (s *Store) AuditFor(id string) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Event{}
	for _, e := range s.s.Audit {
		if e.Proposal == id {
			out = append(out, e)
		}
	}
	return out
}

// Verify walks the audit chain.
func (s *Store) Verify() Verification {
	s.mu.Lock()
	defer s.mu.Unlock()
	return VerifyChain(s.s.Audit, s.s.Migrated != nil)
}

// VerifyChain checks that every event's hash matches its content and links
// to the previous one.
func VerifyChain(ev []Event, migrated bool) Verification {
	v := Verification{OK: true, Events: len(ev), Migrated: migrated}
	prev := ""
	for i, e := range ev {
		switch {
		case e.Seq != i+1:
			v.Error = fmt.Sprintf("event %d has sequence %d", i+1, e.Seq)
		case e.PrevHash != prev:
			v.Error = fmt.Sprintf("event %d does not link to event %d", i+1, i)
		case e.digest() != e.Hash:
			v.Error = fmt.Sprintf("event %d was modified", i+1)
		}
		if v.Error != "" {
			v.OK, v.BrokenAt = false, i+1
			return v
		}
		prev = e.Hash
	}
	v.Head = prev
	return v
}

// expired reports whether p's current deadline has passed.
func (s *Store) expired(p *Proposal) bool {
	return p.ExpiresAt != nil && !s.now().Before(*p.ExpiresAt) && (p.Status == Pending || p.Status == Approved)
}

func (s *Store) expire(p *Proposal, by string) {
	from := p.Status
	note := "approval expired before execution"
	if from == Pending {
		note = "proposal expired without enough approvals"
	}
	p.Status, p.Phase = Expired, PhaseExpired
	s.audit(p, from, Expired, by, note)
}

// ExpireDue marks pending and approved proposals past their deadline as
// expired and returns their ids.
func (s *Store) ExpireDue() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, p := range s.s.Proposals {
		if s.expired(p) {
			s.expire(p, "zyntra")
			ids = append(ids, p.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return ids, s.save()
}

// Decide approves or rejects a pending proposal in one step, ignoring the
// quorum. Rejections always use it.
func (s *Store) Decide(id string, approve bool, by, reason string) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.find(id)
	if err != nil {
		return Proposal{}, err
	}
	if s.expired(p) {
		s.expire(p, "zyntra")
		_ = s.save()
	}
	if p.Status != Pending {
		return Proposal{}, fmt.Errorf("proposal %s is %s, not pending", id, p.Status)
	}
	to, ph := Rejected, PhaseRejected
	now := s.now().UTC()
	if approve {
		to, ph = Approved, PhaseApproved
		p.Approvals = append(p.Approvals, Approval{By: by, At: now, Reason: reason})
		s.approvedDeadline(p, now)
	}
	p.Status, p.Phase, p.DecidedAt, p.DecidedBy, p.Reason = to, ph, &now, by, reason
	s.audit(p, Pending, to, by, reason)
	return clone(p), s.save()
}

func (s *Store) approvedDeadline(p *Proposal, now time.Time) {
	p.ExpiresAt = nil
	if p.Policy != nil && p.Policy.ApprovedExpiry > 0 {
		exp := now.Add(p.Policy.ApprovedExpiry)
		p.ExpiresAt = &exp
	}
}

// Approve records one approval. When the proposal's quorum is reached it
// moves to approved and the second return value is true. Approvers must be
// distinct, and with DistinctFromProposer the proposer cannot approve.
func (s *Store) Approve(id string, a Approval) (Proposal, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.find(id)
	if err != nil {
		return Proposal{}, false, err
	}
	if s.expired(p) {
		s.expire(p, "zyntra")
		_ = s.save()
	}
	if p.Status != Pending {
		return Proposal{}, false, fmt.Errorf("proposal %s is %s, not pending", id, p.Status)
	}
	if p.Policy != nil && p.Policy.DistinctFromProposer && a.By == p.CreatedBy {
		return Proposal{}, false, fmt.Errorf("%w: %s proposed this and cannot also approve it", ErrForbidden, a.By)
	}
	for _, x := range p.Approvals {
		if x.By == a.By {
			return Proposal{}, false, fmt.Errorf("%w: %s has already approved", ErrForbidden, a.By)
		}
	}
	now := s.now().UTC()
	a.At = now
	p.Approvals = append(p.Approvals, a)
	need := max(p.RequiredApprovals, 1)
	if len(p.Approvals) < need {
		s.audit(p, Pending, Pending, a.By, fmt.Sprintf("approval %d of %d: %s", len(p.Approvals), need, a.Reason))
		return clone(p), false, s.save()
	}
	p.Status, p.Phase, p.DecidedAt, p.DecidedBy, p.Reason = Approved, PhaseApproved, &now, a.By, a.Reason
	s.approvedDeadline(p, now)
	note := a.Reason
	if need > 1 {
		note = fmt.Sprintf("quorum %d of %d reached: %s", len(p.Approvals), need, a.Reason)
	}
	s.audit(p, Pending, Approved, a.By, note)
	return clone(p), true, s.save()
}

// Update applies f to a proposal under the lock without auditing (for
// references such as Keep sessions).
func (s *Store) Update(id string, f func(*Proposal)) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.find(id)
	if err != nil {
		return Proposal{}, err
	}
	f(p)
	return clone(p), s.save()
}

// Record applies f and audits the change with note.
func (s *Store) Record(id, by, note string, f func(*Proposal)) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.find(id)
	if err != nil {
		return Proposal{}, err
	}
	from := p.Status
	f(p)
	s.audit(p, from, p.Status, by, note)
	return clone(p), s.save()
}

// Begin re-checks an approved proposal right before it runs. It fails if
// the approval has expired. Proposals are claimed so that two callers
// cannot run the same one.
func (s *Store) Begin(id string) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.find(id)
	if err != nil {
		return Proposal{}, err
	}
	if s.expired(p) {
		s.expire(p, "zyntra")
		_ = s.save()
	}
	if p.Status != Approved {
		return Proposal{}, fmt.Errorf("proposal %s is %s, not approved", id, p.Status)
	}
	return clone(p), nil
}

// Block stops an approved proposal from running and records why.
func (s *Store) Block(id string, rv Revalidation, by string) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.find(id)
	if err != nil {
		return Proposal{}, err
	}
	if p.Status != Approved {
		return Proposal{}, fmt.Errorf("proposal %s is %s, not approved", id, p.Status)
	}
	r := rv
	p.Revalidation, p.BlockedReasons = &r, rv.Reasons
	p.Status, p.Phase = Blocked, PhaseBlocked
	s.audit(p, Approved, Blocked, by, "revalidation failed: "+join(rv.Reasons))
	return clone(p), s.save()
}

func join(s []string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Complete records the execution result of an approved proposal.
func (s *Store) Complete(id string, res executor.Result, by string) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.find(id)
	if err != nil {
		return Proposal{}, err
	}
	if p.Status != Approved {
		return Proposal{}, fmt.Errorf("proposal %s is %s, not approved", id, p.Status)
	}
	to := Executed
	note := string(res.Mode)
	if !res.OK {
		to = Failed
		note += ": " + res.Error
	}
	now := s.now().UTC()
	r := res
	p.Status, p.Execution, p.ExecutedAt, p.ExpiresAt = to, &r, &now, nil
	p.Phase = phaseFor(to, &r)
	payload := res.PayloadHash
	if payload == "" {
		payload = renderHash(p.Render)
	}
	s.auditHashes(p, Approved, to, by, note, payload, res.ResponseHash)
	return clone(p), s.save()
}

// Explain stores the explanation of an outcome verdict and audits its hash.
func (s *Store) Explain(id, by string, ex outcome.Explanation) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.find(id)
	if err != nil {
		return Proposal{}, err
	}
	p.Explanation = &ex
	note := "outcome explained"
	if len(ex.Findings) > 0 {
		note += ": " + ex.Findings[0].Text
	}
	if len(note) > 300 {
		note = note[:300]
	}
	s.auditHashes(p, p.Status, p.Status, by, note, "", "")
	s.s.Audit[len(s.s.Audit)-1].Explanation = ex.Hash
	last := &s.s.Audit[len(s.s.Audit)-1]
	last.Hash = last.digest()
	return clone(p), s.save()
}

// RecordActual stores post-execution KPI values for predicted-vs-actual.
func (s *Store) RecordActual(id string, actual map[string]float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.find(id)
	if err != nil {
		return err
	}
	p.Actual = actual
	return s.save()
}

// Observing returns the ids of proposals with an open outcome observation.
func (s *Store) Observing() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, p := range s.s.Proposals {
		if p.Outcome != nil && !p.Outcome.Done() {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

// ObjectOutcome says whether the business objects an action targeted are
// safe after it ran.
type ObjectOutcome struct {
	CheckedAt   time.Time       `json:"checked_at"`
	Safe        bool            `json:"safe"`
	StillAtRisk []ontology.Risk `json:"still_at_risk,omitempty"`
}
