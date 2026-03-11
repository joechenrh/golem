package agent

import (
	"context"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/llm"
	"github.com/joechenrh/golem/internal/stringutil"
	"github.com/joechenrh/golem/internal/tape"
)

// NudgeClassifier wraps the lightweight classifier LLM to decide whether
// an ambiguous agent response should be nudged, accepted, or escalated.
type NudgeClassifier struct {
	classifierLLM   llm.Client
	classifierModel string
	logger          *zap.Logger
}

// Classify runs the classifier to decide how to handle the response.
// Returns (decision, taskSummary, ok). When ok is false the caller should
// accept the response as-is.
func (nc *NudgeClassifier) Classify(
	ctx context.Context, resp *llm.ChatResponse,
	tapeStore tape.Store, toolNames []string, lastUserMsg string,
) (decision string, taskSummary string, ok bool) {
	if nc.classifierLLM == nil || !isAmbiguousResponse(resp.Content, tapeStore) {
		return "", "", false
	}

	nc.logger.Debug("invoking classifier",
		zap.Int("resp_len", len(resp.Content)))
	dec, ts, rawBody, classOK := classifyResponse(
		ctx, nc.classifierLLM, nc.classifierModel,
		lastUserMsg, resp.Content, toolNames,
	)
	if !classOK {
		nc.logger.Warn("classifier returned unparseable response, accepting",
			zap.String("raw_body", stringutil.Truncate(rawBody, 200)))
		return "", "", false
	}

	nc.logger.Debug("classifier decision",
		zap.String("decision", dec),
		zap.String("task_summary", ts))

	return dec, ts, true
}
