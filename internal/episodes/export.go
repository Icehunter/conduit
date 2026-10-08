package episodes

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/icehunter/conduit/internal/rtk"
)

const (
	SourceExplicitHuman = "explicit_human"
	SourceReviewerModel = "reviewer_model"
	SourceVerification  = "verification"
)

type Start struct {
	RepoID     string `json:"repo_id"`
	TaskID     string `json:"task_id"`
	SplitGroup string `json:"split_group"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request stores model-visible context. ContextComplete requires the caller to
// include router-expanded file content and system/tool context; guessed context
// is not sufficient. Tool-using trajectories need a richer future schema and are
// conservatively withheld by this text-only exporter.
type Request struct {
	Messages        []Message      `json:"messages"`
	System          string         `json:"system,omitempty"`
	ContextComplete bool           `json:"context_complete"`
	ProviderRef     string         `json:"provider_ref,omitempty"`
	EndpointID      string         `json:"endpoint_id,omitempty"`
	ModelID         string         `json:"model_id,omitempty"`
	ModelRevision   string         `json:"model_revision,omitempty"`
	Parameters      map[string]any `json:"parameters,omitempty"`
	Trace           *RequestTrace  `json:"trace,omitempty"`
}

// RequestTrace is router provenance. The stable endpoint identity participates
// in conditioning; per-call IDs, usage and finish reason do not. It holds no raw
// endpoint URL, credentials, or private reasoning from the upstream result.
type RequestTrace struct {
	Version      int            `json:"version"`
	RequestID    string         `json:"request_id"`
	EndpointID   string         `json:"endpoint_id"`
	FinishReason string         `json:"finish_reason"`
	Usage        map[string]any `json:"usage"`
}

type Candidate struct {
	ID              string `json:"id"`
	RequestID       string `json:"request_id"`
	ContextDigest   string `json:"context_digest"`
	BaseDigest      string `json:"base_digest"`
	WorkspaceDigest string `json:"workspace_digest"`
	Completion      string `json:"completion"`
	Patch           string `json:"patch"`
	Role            string `json:"role"`
	ModelID         string `json:"model_id"`
}

type Verification struct {
	CandidateID     string `json:"candidate_id"`
	WorkspaceDigest string `json:"workspace_digest"`
	PolicyDigest    string `json:"policy_digest"`
	Passed          bool   `json:"passed"`
}

type Decision struct {
	CandidateID string `json:"candidate_id"`
	Action      string `json:"action"`
	Source      string `json:"source"`
	Reason      string `json:"reason,omitempty"`
}

type End struct {
	Complete bool   `json:"complete"`
	Status   string `json:"status,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// ContextDigest binds a completion to its actual conditioning input. A repaired
// candidate prompted with new test failures has a different digest, even when
// the original task and base commit match.
func ContextDigest(r Request) string {
	endpointID := ""
	if r.Trace != nil {
		endpointID = r.Trace.EndpointID
	}
	r.Trace = nil
	return digest(struct {
		Request
		ActualEndpointID string `json:"actual_endpoint_id,omitempty"`
	}{Request: r, ActualEndpointID: endpointID})
}

func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type ExportOptions struct {
	HeldOutGroups    []string
	AllowModelReview bool
}

type ExampleMeta struct {
	EpisodeID     string `json:"episode_id"`
	CandidateID   string `json:"candidate_id"`
	SplitGroup    string `json:"split_group"`
	Split         string `json:"split"`
	ContextDigest string `json:"context_digest"`
	BaseDigest    string `json:"base_digest"`
	LabelSource   string `json:"label_source"`
	ModelID       string `json:"model_id"`
}

type SFTExample struct {
	Messages []Message   `json:"messages"`
	Meta     ExampleMeta `json:"meta"`
}
type DPOExample struct {
	Prompt              []Message   `json:"prompt"`
	Chosen              []Message   `json:"chosen"`
	Rejected            []Message   `json:"rejected"`
	RejectedCandidateID string      `json:"rejected_candidate_id"`
	RejectedEpisodeID   string      `json:"rejected_episode_id"`
	Meta                ExampleMeta `json:"meta"`
}

type ExportManifest struct {
	Version          int      `json:"version"`
	Digest           string   `json:"digest"`
	PolicyDigest     string   `json:"policy_digest"`
	SFTCount         int      `json:"sft_count"`
	DPOCount         int      `json:"dpo_count"`
	ExcludedEpisodes []string `json:"excluded_episodes"`
}

type Dataset struct {
	SFT      []SFTExample   `json:"sft"`
	DPO      []DPOExample   `json:"dpo"`
	Manifest ExportManifest `json:"manifest"`
}

type episodeState struct {
	start      Start
	started    bool
	ended      bool
	invalid    bool
	evaluation bool
	requests   map[string]Request
	candidates map[string]Candidate
	checks     map[string]Verification
	decisions  map[string]Decision
}

type preferenceKey struct{ repo, context, base, split string }
type preferenceRow struct {
	key       preferenceKey
	candidate Candidate
	prompt    []Message
	meta      ExampleMeta
}

func ExportFile(path string, options ExportOptions) (Dataset, error) {
	return ExportFiles([]string{path}, options)
}

// ExportFiles combines independently validated journals. Replayed event IDs
// are rejected, preventing duplicate logs from silently duplicating examples.
func ExportFiles(paths []string, options ExportOptions) (Dataset, error) {
	if len(paths) == 0 {
		return Dataset{}, errors.New("episodes: at least one ledger required")
	}
	var all []Event
	seen := map[string]bool{}
	for _, path := range paths {
		events, err := ReadFile(path)
		if err != nil {
			return Dataset{}, err
		}
		for _, event := range events {
			if seen[event.ID] {
				return Dataset{}, errors.New("episodes: duplicate event across input ledgers")
			}
			seen[event.ID] = true
		}
		all = append(all, events...)
	}
	return Export(all, options)
}

// Export is deterministic and fail-closed. It never interprets an assistant's
// success claim, test passage, timeout or implicit UI dismissal as acceptance.
func Export(events []Event, options ExportOptions) (Dataset, error) {
	states := map[string]*episodeState{}
	for _, e := range events {
		if e.EpisodeID == "" {
			return Dataset{}, errors.New("episodes: export missing episode ID")
		}
		s := states[e.EpisodeID]
		if s == nil {
			s = &episodeState{requests: map[string]Request{}, candidates: map[string]Candidate{}, checks: map[string]Verification{}, decisions: map[string]Decision{}}
			states[e.EpisodeID] = s
		}
		// Explicit offline decisions may arrive after execution closes. They do
		// not reopen the task; any later model/tool event invalidates it.
		if s.ended && e.Kind != KindDecision {
			s.invalid = true
		}
		s.invalid = s.invalid || e.Incomplete || e.Redacted
		if e.Origin == OriginEvaluation {
			s.evaluation = true
		} else if e.Origin != "" && e.Origin != OriginInteractive {
			s.invalid = true
		}
		if e.Kind != KindStart && !s.started {
			s.invalid = true
		}
		// Content-policy omission is a valid recorder outcome. Never parse its
		// placeholder as the original typed payload or poison unrelated episodes.
		if e.Incomplete || e.Redacted {
			continue
		}
		var err error
		switch e.Kind {
		case KindStart:
			if s.started {
				s.invalid = true
			}
			s.started = true
			err = decodePayload(e.Payload, &s.start)
		case KindRequest:
			var request Request
			err = decodePayload(e.Payload, &request)
			if e.RequestID == "" {
				s.invalid = true
			}
			if _, ok := s.requests[e.RequestID]; ok {
				s.invalid = true
			}
			s.requests[e.RequestID] = request
			if !request.ContextComplete {
				s.invalid = true
			}
		case KindCandidate:
			var c Candidate
			err = decodePayload(e.Payload, &c)
			if _, ok := s.candidates[c.ID]; ok {
				s.invalid = true
			}
			s.candidates[c.ID] = c
		case KindVerification:
			var v Verification
			err = decodePayload(e.Payload, &v)
			s.checks[v.CandidateID] = v
		case KindDecision:
			var d Decision
			err = decodePayload(e.Payload, &d)
			if _, ok := s.candidates[d.CandidateID]; !ok {
				s.invalid = true
			}
			s.decisions[d.CandidateID] = d
		case KindEnd:
			var end End
			err = decodePayload(e.Payload, &end)
			s.ended = true
			if !end.Complete {
				s.invalid = true
			}
		}
		if err != nil {
			return Dataset{}, fmt.Errorf("episodes: decode %s: %w", e.Kind, err)
		}
	}
	forbidden := map[string]bool{}
	for _, group := range options.HeldOutGroups {
		forbidden[group] = true
	}
	unknownEvaluationGroup := false
	for _, s := range states {
		if s.evaluation {
			if s.start.SplitGroup == "" {
				unknownEvaluationGroup = true
			}
			forbidden[s.start.SplitGroup] = true
		}
	}
	ids := make([]string, 0, len(states))
	for id := range states {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	dataset := Dataset{SFT: []SFTExample{}, DPO: []DPOExample{}, Manifest: ExportManifest{Version: Version, ExcludedEpisodes: []string{}}}
	var chosenRows []preferenceRow
	rejectedRows := map[preferenceKey][]preferenceRow{}
	for _, id := range ids {
		s := states[id]
		if unknownEvaluationGroup || !s.started || !s.ended || s.invalid || s.evaluation || s.start.RepoID == "" || s.start.TaskID == "" || s.start.SplitGroup == "" || forbidden[s.start.SplitGroup] {
			dataset.Manifest.ExcludedEpisodes = append(dataset.Manifest.ExcludedEpisodes, id)
			continue
		}
		candidateIDs := make([]string, 0, len(s.candidates))
		for cid := range s.candidates {
			candidateIDs = append(candidateIDs, cid)
		}
		sort.Strings(candidateIDs)
		for _, cid := range candidateIDs {
			c := s.candidates[cid]
			req, ok := s.requests[c.RequestID]
			if !ok || !validCandidate(c, req) {
				continue
			}
			d := s.decisions[cid]
			prompt := requestMessages(req)
			meta := ExampleMeta{EpisodeID: id, CandidateID: cid, SplitGroup: s.start.SplitGroup, Split: GroupSplit(s.start.RepoID, s.start.SplitGroup), ContextDigest: c.ContextDigest, BaseDigest: c.BaseDigest, LabelSource: d.Source, ModelID: c.ModelID}
			key := preferenceKey{s.start.RepoID, c.ContextDigest, c.BaseDigest, meta.Split}
			if d.Action == "rejected" && d.Source == SourceExplicitHuman {
				rejectedRows[key] = append(rejectedRows[key], preferenceRow{key, c, prompt, meta})
			}
			check := s.checks[cid]
			if d.Action != "accepted" || (d.Source != SourceExplicitHuman && !(options.AllowModelReview && d.Source == SourceReviewerModel)) || !check.Passed || check.WorkspaceDigest != c.WorkspaceDigest || check.PolicyDigest == "" {
				continue
			}
			messages := append(append([]Message(nil), prompt...), Message{Role: "assistant", Content: c.Completion})
			dataset.SFT = append(dataset.SFT, SFTExample{Messages: messages, Meta: meta})
			if d.Source != SourceExplicitHuman {
				continue
			}
			chosenRows = append(chosenRows, preferenceRow{key, c, prompt, meta})
		}
	}
	// Independently sampled episodes can form preferences; repairs with changed
	// conditioning cannot. Rejected candidates never cross dataset splits.
	for _, chosen := range chosenRows {
		for _, rejected := range rejectedRows[chosen.key] {
			if rejected.candidate.Completion == chosen.candidate.Completion {
				continue
			}
			dataset.DPO = append(dataset.DPO, DPOExample{Prompt: chosen.prompt, Chosen: []Message{{Role: "assistant", Content: chosen.candidate.Completion}}, Rejected: []Message{{Role: "assistant", Content: rejected.candidate.Completion}}, RejectedCandidateID: rejected.candidate.ID, RejectedEpisodeID: rejected.meta.EpisodeID, Meta: chosen.meta})
		}
	}
	groups := append([]string(nil), options.HeldOutGroups...)
	sort.Strings(groups)
	options.HeldOutGroups = groups
	dataset.Manifest.PolicyDigest = digest(options)
	dataset.Manifest.SFTCount = len(dataset.SFT)
	dataset.Manifest.DPOCount = len(dataset.DPO)
	dataset.Manifest.Digest = digest(struct {
		SFT    []SFTExample
		DPO    []DPOExample
		Policy string
	}{dataset.SFT, dataset.DPO, dataset.Manifest.PolicyDigest})
	return dataset, nil
}

func decodePayload(value any, target any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func requestMessages(r Request) []Message {
	messages := []Message{}
	if r.System != "" {
		messages = append(messages, Message{Role: "system", Content: r.System})
	}
	return append(messages, r.Messages...)
}

func validCandidate(c Candidate, r Request) bool {
	if c.ID == "" || c.RequestID == "" || c.Role != "implement" || c.ModelID == "" || c.BaseDigest == "" || c.WorkspaceDigest == "" || c.Completion == "" || c.Patch == "" || !r.ContextComplete || c.ContextDigest != ContextDigest(r) || len(r.Messages) == 0 {
		return false
	}
	if r.Messages[len(r.Messages)-1].Role != "user" {
		return false
	}
	stringsToCheck := []string{r.System, c.Completion, c.Patch}
	for _, m := range r.Messages {
		if m.Role != "user" && m.Role != "assistant" && m.Role != "system" {
			return false
		}
		stringsToCheck = append(stringsToCheck, m.Content)
	}
	for _, s := range stringsToCheck {
		if strings.Contains(s, "[REDACTED:") || strings.Contains(s, "[OMITTED:") || privateReasoning.MatchString(s) || recorderAssignment.MatchString(s) {
			return false
		}
		if _, n := rtk.RedactSecrets(s); n > 0 {
			return false
		}
	}
	return true
}

// GroupSplit keeps all episodes for one repository/task group together. Held-out
// groups are explicit exclusions; this hash only selects train versus validation.
func GroupSplit(repoID, group string) string {
	sum := sha256.Sum256([]byte(repoID + "\x00" + group))
	if sum[0] < 26 {
		return "validation"
	}
	return "train"
}

// WriteJSONL emits standard conversational training rows; split must be train
// or validation, preventing accidental mixing of evaluation data.
func (d Dataset) WriteJSONL(w io.Writer, kind, split string) error {
	if split != "train" && split != "validation" {
		return errors.New("episodes: split must be train or validation")
	}
	encoder := json.NewEncoder(w)
	switch kind {
	case "sft":
		for _, row := range d.SFT {
			if row.Meta.Split == split {
				if err := encoder.Encode(row); err != nil {
					return fmt.Errorf("episodes: export SFT: %w", err)
				}
			}
		}
	case "dpo":
		for _, row := range d.DPO {
			if row.Meta.Split == split {
				if err := encoder.Encode(row); err != nil {
					return fmt.Errorf("episodes: export DPO: %w", err)
				}
			}
		}
	default:
		return errors.New("episodes: format must be sft or dpo")
	}
	return nil
}
