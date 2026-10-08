package compact

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/transcript"
)

// defaultTailTokens is the verbatim tail kept when Options.TailTokens is unset.
const defaultTailTokens = 20_000

// errNothingToCompact means everything before the verbatim tail is empty or is
// only the previous compaction summary, so a new summary could add nothing.
var errNothingToCompact = errors.New("compact: nothing to compact")

// IsNothingToCompact reports whether err is the "nothing to compact" outcome:
// not a failure, but also not something retrying will fix.
func IsNothingToCompact(err error) bool { return errors.Is(err, errNothingToCompact) }

// tailBudget returns the token budget for the verbatim tail:
// min(TailTokens or 20K, MainWindow/5).
func tailBudget(o Options) int {
	budget := o.TailTokens
	if budget <= 0 {
		budget = defaultTailTokens
	}
	if o.MainWindow > 0 && o.MainWindow/5 < budget {
		budget = o.MainWindow / 5
	}
	return budget
}

// estimateMessageTokens approximates a message's token cost as len(json)/4.
func estimateMessageTokens(m api.Message) int {
	b, err := json.Marshal(m)
	if err != nil {
		return 0
	}
	return len(b) / 4
}

// splitTail returns the index of the first message of the verbatim tail.
//
// The split always lands on an assistant message. A tool_use lives in an
// assistant message and its tool_result in the user message right after it, so
// a tail that starts on an assistant message can never separate a pair. The
// tail is the longest suffix, starting at an assistant message, that fits in
// budgetTokens; when even the last assistant message does not fit, that message
// (and whatever follows it) is still kept. With no assistant message at all the
// tail is empty and the result is len(msgs).
func splitTail(msgs []api.Message, budgetTokens int) int {
	split := len(msgs)
	sum := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		sum += estimateMessageTokens(msgs[i])
		if msgs[i].Role != "assistant" {
			continue
		}
		if split != len(msgs) && sum > budgetTokens {
			break
		}
		split = i
	}
	return split
}

// tailResultFloors are the per-result character caps tried, largest first,
// when the forced tail exceeds its budget.
var tailResultFloors = []int{8000, 2000, 500}

// shrinkTail returns a copy of tail whose size is brought under budgetTokens
// by shortening oversized tool_result content. Messages and blocks are never
// removed, so tool_use/tool_result pairs stay intact; a shortened result keeps
// its head and a marker stating the original size. The input is not modified.
func shrinkTail(tail []api.Message, budgetTokens int) []api.Message {
	out := make([]api.Message, len(tail))
	for i, m := range tail {
		m.Content = append([]api.ContentBlock(nil), m.Content...)
		out[i] = m
	}
	size := func() int {
		n := 0
		for _, m := range out {
			n += estimateMessageTokens(m)
		}
		return n
	}
	for _, floor := range tailResultFloors {
		if size() <= budgetTokens {
			break
		}
		for i := range out {
			for j, b := range out[i].Content {
				if b.Type != "tool_result" {
					continue
				}
				text := transcript.ToolResultText(b)
				n := utf8.RuneCountInString(text)
				if n <= floor {
					continue
				}
				b.ResultBlocks = nil
				b.ResultContent = transcript.Truncate(text, floor) +
					fmt.Sprintf("\n[… tool output shortened by compaction: %d of %d chars kept …]", floor, n)
				out[i].Content[j] = b
			}
		}
	}
	return out
}
