package parser

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// LLM turns (system, user) into the model's raw JSON text. Tests use a fake.
type LLM interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// Claude calls the Anthropic Messages API with structured outputs, so the
// reply is constrained to Schema. The model comes from ANTHROPIC_MODEL.
type Claude struct {
	client anthropic.Client
	model  string
}

func NewClaude(apiKey, model string, opts ...option.RequestOption) *Claude {
	opts = append([]option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithRequestTimeout(30 * time.Second),
		option.WithMaxRetries(2),
	}, opts...)
	return &Claude{client: anthropic.NewClient(opts...), model: model}
}

// ErrRefused means the model declined to answer (stop_reason "refusal").
var ErrRefused = errors.New("model refused the request")

func (c *Claude) Complete(ctx context.Context, system, user string) (string, error) {
	resp, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: 4096, // includes any thinking; the JSON itself is small
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(user))},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffortLow, // a to-do parser doesn't need deep thought
			Format: anthropic.JSONOutputFormatParam{Schema: Schema},
		},
	})
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return "", fmt.Errorf("anthropic api: status %d: %w", apiErr.StatusCode, err)
		}
		return "", fmt.Errorf("anthropic api: %w", err)
	}
	switch resp.StopReason {
	case anthropic.StopReasonRefusal:
		return "", ErrRefused
	case anthropic.StopReasonMaxTokens:
		return "", errors.New("anthropic api: reply cut off at max_tokens")
	}
	var b strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String(), nil
}

// Schema is the JSON contract from docs/SPEC.md, as a structured-outputs schema.
// The API enforces its shape; Parser.validate still checks meaning (ids, dates).
var Schema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"actions": map[string]any{
			"type": "array",
			"items": map[string]any{
				"anyOf": []any{
					action("add", map[string]any{
						"text": map[string]any{"type": "string"},
						"date": map[string]any{"type": "string", "format": "date"},
					}),
					action("done", map[string]any{"task_id": map[string]any{"type": "integer"}}),
					action("remove", map[string]any{"task_id": map[string]any{"type": "integer"}}),
					action("move", map[string]any{
						"task_id": map[string]any{"type": "integer"},
						"date":    map[string]any{"type": "string", "format": "date"},
					}),
				},
			},
		},
		"clarification": map[string]any{
			"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
		},
	},
	"required":             []string{"actions", "clarification"},
	"additionalProperties": false,
}

func action(kind string, fields map[string]any) map[string]any {
	props := map[string]any{"type": map[string]any{"type": "string", "const": kind}}
	required := []string{"type"}
	for k, v := range fields {
		props[k] = v
		required = append(required, k)
	}
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	}
}
