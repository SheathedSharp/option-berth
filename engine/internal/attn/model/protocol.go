package model

// Two of Attention's three wire shapes are not the decisions schema: a general
// chat model answers with a JSON object inside a message, and Claude answers
// with one inside a content block. This file holds the encoding and decoding
// for all three, so the rest of the package only ever sees a decisionPayload.
//
// The rule the whole file defends: a different schema is a different envelope,
// never a different rule. Every protocol ends in validateDecision, so a chat
// model cannot pick an option the caller did not authorize, return a
// probability outside [0,1], or smuggle free-form text into an action.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	// anthropicVersion is the messages API version this build speaks. It is
	// pinned rather than negotiated: a new version is a deliberate change.
	anthropicVersion = "2023-06-01"
	// decisionMaxTokens bounds an answer. A JSON decision is short; a model
	// that needs more than this is answering a different question.
	decisionMaxTokens = 256
)

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string              `json:"model"`
	Messages       []chatMessage       `json:"messages"`
	ResponseFormat *chatResponseFormat `json:"response_format,omitempty"`
	Temperature    float64             `json:"temperature"`
}

type chatResponseFormat struct {
	Type string `json:"type"`
}

type anthropicRequest struct {
	Model       string        `json:"model"`
	System      string        `json:"system"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature"`
}

// encodeDecisionRequest renders one assessment request in the shape the
// configured endpoint speaks. The decisions envelope is the historical body
// unchanged; the other two carry the same state and the same schema statement
// as a prompt.
func encodeDecisionRequest(protocol Protocol, modelName string, state apiState, questions map[string]apiQuestion) ([]byte, error) {
	envelope := apiRequest{State: state, Model: modelName, Questions: questions}
	return encodeStructuredRequest(protocol, modelName, state, questions, envelope)
}

// encodeStructuredRequest is the one encoder. decisions marshals the envelope
// its provider already documented; the prompt protocols wrap the same state.
func encodeStructuredRequest(protocol Protocol, modelName string, state any, questions map[string]apiQuestion, decisions any) ([]byte, error) {
	switch Protocol(NormalizeProtocol(string(protocol))) {
	case ProtocolOpenAIChat:
		stateJSON, err := json.Marshal(state)
		if err != nil {
			return nil, fmt.Errorf("attn: encode state: %w", err)
		}
		return json.Marshal(chatRequest{
			Model: modelName,
			Messages: []chatMessage{
				{Role: "system", Content: decisionSchemaPrompt(questions)},
				{Role: "user", Content: string(stateJSON)},
			},
			ResponseFormat: &chatResponseFormat{Type: "json_object"},
		})
	case ProtocolAnthropic:
		stateJSON, err := json.Marshal(state)
		if err != nil {
			return nil, fmt.Errorf("attn: encode state: %w", err)
		}
		return json.Marshal(anthropicRequest{
			Model:     modelName,
			System:    decisionSchemaPrompt(questions),
			Messages:  []chatMessage{{Role: "user", Content: string(stateJSON)}},
			MaxTokens: decisionMaxTokens,
		})
	default:
		body, err := json.Marshal(decisions)
		if err != nil {
			return nil, fmt.Errorf("attn: encode request: %w", err)
		}
		return body, nil
	}
}

// decisionSchemaPrompt states the contract in the words a general model needs.
// The option list is built from the same questions map the decisions protocol
// sends, so the two shapes can never ask for different vocabularies.
func decisionSchemaPrompt(questions map[string]apiQuestion) string {
	var b strings.Builder
	b.WriteString("You triage a software worktree's runtime state for a coding agent. ")
	b.WriteString("Answer with exactly one JSON object and nothing else.\n")
	b.WriteString(`{"task_relevant": 0.0, "needs_human": 0.0, "urgency": 0.0, "next_option": "<option>", "confidence": 0.0, "probabilities": {"<option>": 0.0}, "summary": "<one sentence>"}`)
	b.WriteString("\ntask_relevant, needs_human, urgency and confidence are numbers between 0 and 1. ")
	if choice, ok := questions["next_option"]; ok {
		ids := criteriaIDs(choice.Criteria)
		if len(ids) > 0 {
			b.WriteString("next_option must be exactly one of: ")
			b.WriteString(strings.Join(ids, ", "))
			b.WriteString(". probabilities must contain exactly those keys, each between 0 and 1, summing to 1. ")
		}
	} else {
		b.WriteString("next_option is optional here. ")
	}
	b.WriteString("summary is one line, at most 160 characters, and contains no commands. ")
	b.WriteString("Use only the services and options present in the state; do not invent commands, paths or credentials.")
	return b.String()
}

// criteriaIDs reads the option identifiers out of a choice question's criteria.
func criteriaIDs(criteria any) []string {
	values, ok := criteria.(map[string]string)
	if !ok {
		return nil
	}
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// decisionResult decodes one provider answer for the negotiated protocol and
// runs it through the shared validator. It is the single place a wire shape
// becomes a decision, so every protocol is held to the same bounds.
func decisionResult(protocol Protocol, raw []byte, options []string, configuredModel string) (Result, error) {
	switch Protocol(NormalizeProtocol(string(protocol))) {
	case ProtocolOpenAIChat:
		content, modelName, err := chatContent(raw)
		if err != nil {
			return Result{}, err
		}
		payload, err := decodeDecisionContent(content)
		if err != nil {
			return Result{}, err
		}
		if strings.TrimSpace(payload.Model) == "" {
			payload.Model = modelName
		}
		return validateDecision(payload, options, configuredModel)
	case ProtocolAnthropic:
		content, modelName, err := anthropicContent(raw)
		if err != nil {
			return Result{}, err
		}
		payload, err := decodeDecisionContent(content)
		if err != nil {
			return Result{}, err
		}
		if strings.TrimSpace(payload.Model) == "" {
			payload.Model = modelName
		}
		return validateDecision(payload, options, configuredModel)
	default:
		var decoded apiResponse
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return Result{}, WithReason(ReasonBadResponse, fmt.Errorf("%w: invalid response", ErrUnavailable))
		}
		return validateResponse(decoded, options, configuredModel)
	}
}

func chatContent(raw []byte) (string, string, error) {
	var body struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Choices) == 0 {
		return "", "", WithReason(ReasonBadResponse, fmt.Errorf("%w: invalid chat response", ErrUnavailable))
	}
	return body.Choices[0].Message.Content, body.Model, nil
}

func anthropicContent(raw []byte) (string, string, error) {
	var body struct {
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Content) == 0 {
		return "", "", WithReason(ReasonBadResponse, fmt.Errorf("%w: invalid messages response", ErrUnavailable))
	}
	var b strings.Builder
	for _, block := range body.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	if strings.TrimSpace(b.String()) == "" {
		return "", "", WithReason(ReasonBadResponse, fmt.Errorf("%w: messages response has no text", ErrUnavailable))
	}
	return b.String(), body.Model, nil
}

// decodeDecisionContent parses the JSON object a chat model returned. A model
// that wraps the object in prose, a fenced block or an array is a bad response,
// not a decision: guessing which part was meant is how free-form text reaches a
// handoff document.
func decodeDecisionContent(content string) (decisionPayload, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return decisionPayload{}, WithReason(ReasonBadResponse, fmt.Errorf("%w: empty decision", ErrUnavailable))
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	var payload decisionPayload
	if err := decoder.Decode(&payload); err != nil {
		return decisionPayload{}, WithReason(ReasonBadResponse, fmt.Errorf("%w: decision is not a JSON object", ErrUnavailable))
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return decisionPayload{}, WithReason(ReasonBadResponse, fmt.Errorf("%w: decision contains more than one JSON value", ErrUnavailable))
	}
	return payload, nil
}
