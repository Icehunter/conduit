package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/episodes"
	"github.com/icehunter/conduit/internal/evaluation"
	"github.com/icehunter/conduit/internal/procgroup"
	"github.com/icehunter/conduit/internal/verification"
)

const episodesHelp = `Usage:
  conduit episodes export --file SESSION.jsonl [--file OTHER.jsonl ...] [--format sft|dpo] [--split train|validation] [--manifest NEW_FILE]
  conduit episodes accept --file SESSION.jsonl --episode ID --candidate ID [--reason TEXT]
  conduit episodes reject --file SESSION.jsonl --episode ID --candidate ID [--reason TEXT]

Export writes eligible training rows to stdout. --held-out-groups accepts comma-separated groups to exclude.
--allow-model-review opts into reviewer-model SFT labels; DPO still requires explicit human labels.
Accept/reject record your explicit decision, never a model's claimed preference. They do not imply tests passed.
`

func runEpisodes(args []string) error { return episodesCommand(args, os.Stdout) }

type episodeFiles []string

func (f *episodeFiles) String() string { return strings.Join(*f, ",") }
func (f *episodeFiles) Set(value string) error {
	if value == "" {
		return errors.New("episode file must not be empty")
	}
	*f = append(*f, value)
	return nil
}

func episodesCommand(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, err := io.WriteString(out, episodesHelp)
		return err
	}
	switch args[0] {
	case "export":
		return exportEpisodes(args[1:], out)
	case "accept", "reject":
		return labelEpisode(args[0], args[1:], out)
	default:
		return fmt.Errorf("episodes: unknown command %q", args[0])
	}
}

func exportEpisodes(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("episodes export", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var files episodeFiles
	flags.Var(&files, "file", "episode ledger; repeat for multiple sessions")
	format := flags.String("format", "sft", "sft or dpo")
	split := flags.String("split", "train", "train or validation")
	manifest := flags.String("manifest", "", "new manifest path")
	heldout := flags.String("held-out-groups", "", "excluded groups")
	modelReview := flags.Bool("allow-model-review", false, "allow model-reviewed SFT")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("episodes export: %w", err)
	}
	if flags.NArg() != 0 || len(files) == 0 || (*format != "sft" && *format != "dpo") || (*split != "train" && *split != "validation") {
		return errors.New("episodes export: --file, valid --format and --split are required")
	}
	var groups []string
	for _, group := range strings.Split(*heldout, ",") {
		if group = strings.TrimSpace(group); group != "" {
			groups = append(groups, group)
		}
	}
	dataset, err := episodes.ExportFiles(files, episodes.ExportOptions{HeldOutGroups: groups, AllowModelReview: *modelReview})
	if err != nil {
		return err
	}
	if *manifest != "" {
		f, err := os.OpenFile(*manifest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("episodes: create manifest: %w", err)
		}
		encodeErr := json.NewEncoder(f).Encode(dataset.Manifest)
		closeErr := f.Close()
		if err := errors.Join(encodeErr, closeErr); err != nil {
			return fmt.Errorf("episodes: write manifest: %w", err)
		}
	}
	return dataset.WriteJSONL(out, *format, *split)
}

func labelEpisode(action string, args []string, out io.Writer) (retErr error) {
	flags := flag.NewFlagSet("episodes "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	file := flags.String("file", "", "episode ledger")
	episodeID := flags.String("episode", "", "episode ID")
	candidateID := flags.String("candidate", "", "candidate ID")
	reason := flags.String("reason", "", "human explanation")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("episodes label: %w", err)
	}
	if flags.NArg() != 0 || *file == "" || *episodeID == "" || *candidateID == "" {
		return errors.New("episodes label: --file, --episode and --candidate required")
	}
	// Lock before reading so a concurrent runtime cannot mutate the episode
	// between validation and the explicit human decision append.
	path, err := filepath.Abs(*file)
	if err != nil {
		return fmt.Errorf("episodes label: path: %w", err)
	}
	if !strings.HasSuffix(path, ".jsonl") {
		return errors.New("episodes label: ledger must end in .jsonl")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("episodes label: ledger: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("episodes label: cwd: %w", err)
	}
	r, err := episodes.New(episodes.Config{Enabled: true, IncludeContent: true, Dir: filepath.Dir(path), RepositoryRoot: cwd, SessionID: strings.TrimSuffix(filepath.Base(path), ".jsonl")})
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, r.Close()) }()
	events, err := episodes.ReadFile(path)
	if err != nil {
		return err
	}
	known, complete := false, false
	for _, event := range events {
		if event.EpisodeID != *episodeID {
			continue
		}
		switch event.Kind {
		case episodes.KindCandidate:
			var c episodes.Candidate
			b, marshalErr := json.Marshal(event.Payload)
			if marshalErr != nil {
				return marshalErr
			}
			if err := json.Unmarshal(b, &c); err != nil {
				return err
			}
			if c.ID == *candidateID {
				known = true
			}
		case episodes.KindEnd:
			var end episodes.End
			b, marshalErr := json.Marshal(event.Payload)
			if marshalErr != nil {
				return marshalErr
			}
			if err := json.Unmarshal(b, &end); err != nil {
				return err
			}
			complete = end.Complete
		case episodes.KindDecision:
		default:
			if complete {
				complete = false
			}
		}
	}
	if !known || !complete {
		return errors.New("episodes label: candidate must exist in a closed, complete episode")
	}
	decision := "accepted"
	if action == "reject" {
		decision = "rejected"
	}
	if err := r.Record(episodes.Event{Kind: episodes.KindDecision, EpisodeID: *episodeID, Payload: episodes.Decision{CandidateID: *candidateID, Action: decision, Source: episodes.SourceExplicitHuman, Reason: *reason}}); err != nil {
		return err
	}
	if err := r.Close(); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]string{"episode_id": *episodeID, "candidate_id": *candidateID, "decision": decision, "source": episodes.SourceExplicitHuman})
}

const evaluationHelp = `Usage:
  conduit evaluate --manifest TASK.json
  conduit evaluate --manifest TASK.json --execute --authorize-checks --implement-argv '["executable","arg"]'

Without --execute, validates the manifest only. Execution uses a disposable local Git checkout.
The implement command reads the task JSON from stdin, edits only its working directory, and must return
one JSON object on stdout: {"turns":1,"tokens":100,"model_revision":"PINNED_REVISION"}.
It must honor the supplied turn/token budgets and disable training capture and persistent memory.
The command receives CONDUIT_EVAL_ORIGIN=evaluation and CONDUIT_EVAL_DISABLE_MEMORY=1.
This isolates repository state; it is not an OS sandbox. Checks require --authorize-checks.
`

func runEvaluate(args []string) error { return evaluateCommand(args, os.Stdout) }

func evaluateCommand(args []string, out io.Writer) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "--help")) {
		_, err := io.WriteString(out, evaluationHelp)
		return err
	}
	flags := flag.NewFlagSet("evaluate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	manifest := flags.String("manifest", "", "pinned task manifest")
	execute := flags.Bool("execute", false, "execute evaluation")
	authorized := flags.Bool("authorize-checks", false, "authorize exact manifest checks")
	argvJSON := flags.String("implement-argv", "", "JSON executable argv")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("evaluate: %w", err)
	}
	if flags.NArg() != 0 || *manifest == "" {
		return errors.New("evaluate: --manifest required")
	}
	if !*execute && (*argvJSON != "" || *authorized) {
		return errors.New("evaluate: implementation/check execution requires --execute")
	}
	task, err := evaluation.LoadManifest(*manifest)
	if err != nil {
		return err
	}
	if !*execute {
		return json.NewEncoder(out).Encode(map[string]any{"task_id": task.ID, "status": "validated", "executed": false})
	}
	if !*authorized {
		return errors.New("evaluate: --authorize-checks required to run manifest commands")
	}
	var argv []string
	if err := json.Unmarshal([]byte(*argvJSON), &argv); err != nil || len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return errors.New("evaluate: --implement-argv must be a nonempty JSON string array")
	}
	executable, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("evaluate: executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("evaluate: executable path: %w", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	result, runErr := evaluation.Run(ctx, task, evaluation.Options{Authorize: func(context.Context, verification.Check) error { return nil }, Implement: func(ctx context.Context, w evaluation.Workspace) (evaluation.Usage, error) {
		return runEvaluationCommand(ctx, w, executable, argv[1:])
	}})
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return fmt.Errorf("evaluate: output: %w", err)
	}
	if runErr != nil {
		return runErr
	}
	if result.Status != "passed" {
		return fmt.Errorf("evaluate: task ended %s", result.Status)
	}
	return nil
}

type boundedEvaluationOutput struct{ bytes.Buffer }

func (b *boundedEvaluationOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64<<10 {
		return 0, errors.New("evaluate: implementer output exceeds 64 KiB")
	}
	return b.Buffer.Write(p)
}

func runEvaluationCommand(ctx context.Context, w evaluation.Workspace, executable string, args []string) (evaluation.Usage, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = w.Root
	cmd.Env = append(w.Environment, "CONDUIT_EVAL_ORIGIN=evaluation", "CONDUIT_EVAL_DISABLE_MEMORY=1")
	task := w.Task
	task.RepoPath = w.Root
	payload, err := json.Marshal(task)
	if err != nil {
		return evaluation.Usage{}, fmt.Errorf("evaluate: task encode: %w", err)
	}
	cmd.Stdin = bytes.NewReader(payload)
	var output boundedEvaluationOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	procgroup.Set(cmd)
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return evaluation.Usage{}, fmt.Errorf("evaluate: implementer command: %w", err)
	}
	var usage evaluation.Usage
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&usage); err != nil {
		return usage, fmt.Errorf("evaluate: implementer usage JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return usage, errors.New("evaluate: extra implementer output")
	}
	return usage, nil
}
