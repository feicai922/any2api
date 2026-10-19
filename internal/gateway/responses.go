package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"api-gateway/internal/config"
)

// responsesFromChat implements POST /v1/responses for upstreams that only
// provide OpenAI Chat Completions. It supports text, system instructions,
// images, multi-turn conversations, function tools and SSE streaming.
func (g *Gateway) responsesFromChat(w http.ResponseWriter, r *http.Request, project config.ResolvedProject) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": map[string]any{"message": "method not allowed"}})
		return
	}
	obj, _, err := readJSON(r, g.maxBodyBytes)
	if err != nil {
		responseError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	messages, err := responsesInputToMessages(obj["input"], obj["instructions"])
	if err != nil {
		responseError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(messages) == 0 {
		responseError(w, http.StatusBadRequest, "input is required")
		return
	}
	stream := isStream(r, obj)
	chat := map[string]any{
		"model":    modelName(obj),
		"messages": messages,
		"stream":   stream,
	}
	if stream {
		chat["stream_options"] = map[string]any{"include_usage": true}
	}
	copyResponsesParams(obj, chat)
	extra := http.Header{}
	copyClientAuth(r, extra)
	resp, err := g.sendChat(r, project, chat, extra)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorEnvelope("upstream_error", err.Error()))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		forwardAsResponsesError(w, resp.StatusCode, body)
		return
	}
	if stream {
		streamResponses(w, resp.Body, modelName(obj))
		return
	}
	var chatResp map[string]any
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(&chatResp); err != nil {
		writeJSON(w, http.StatusBadGateway, errorEnvelope("upstream_error", "invalid chat response: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, chatToResponse(chatResp, modelName(obj)))
}

func responsesInputToMessages(input any, instructions any) ([]any, error) {
	out := []any{}
	if s, ok := instructions.(string); ok && s != "" {
		out = append(out, map[string]any{"role": "system", "content": s})
	}
	switch v := input.(type) {
	case string:
		if v != "" {
			out = append(out, map[string]any{"role": "user", "content": v})
		}
	case []any:
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			itemType, _ := m["type"].(string)
			role, _ := m["role"].(string)
			switch {
			case itemType == "message" || (role != "" && itemType == ""):
				if role == "" {
					role = "user"
				}
				out = append(out, map[string]any{"role": role, "content": responsesContentToChat(m["content"])})
			case itemType == "function_call":
				call := map[string]any{
					"id": stringValue(m["call_id"]), "type": "function",
					"function": map[string]any{
						"name": stringValue(m["name"]), "arguments": stringValue(m["arguments"]),
					},
				}
				if cid := stringValue(m["call_id"]); cid == "" {
					call["id"] = stringValue(m["id"])
				}
				out = append(out, map[string]any{
					"role": "assistant", "content": nil, "tool_calls": []any{call},
				})
			case itemType == "custom_tool_call":
				out = append(out, map[string]any{
					"role": "assistant", "content": nil,
					"tool_calls": []any{map[string]any{
						"id": stringValue(m["call_id"]), "type": "function",
						"function": map[string]any{
							"name": stringValue(m["name"]), "arguments": stringValue(m["input"]),
						},
					}},
				})
			case itemType == "function_call_output" || itemType == "custom_tool_call_output":
				out = append(out, map[string]any{
					"role": "tool", "tool_call_id": stringValue(m["call_id"]),
					"content": stringValue(m["output"]),
				})
			case itemType == "input_text" || itemType == "text":
				text, _ := m["text"].(string)
				if text != "" {
					out = append(out, map[string]any{"role": "user", "content": text})
				}
			}
		}
	case map[string]any:
		if role, ok := v["role"].(string); ok {
			out = append(out, map[string]any{"role": role, "content": responsesContentToChat(v["content"])})
		}
	default:
		return nil, fmt.Errorf("input must be a string or array")
	}
	return mergeAdjacentToolCalls(out), nil
}

func responsesContentToChat(v any) any {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		parts := []any{}
		for _, raw := range x {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch m["type"] {
			case "input_text", "output_text", "text":
				if t, ok := m["text"].(string); ok {
					parts = append(parts, map[string]any{"type": "text", "text": t})
				}
			case "input_image":
				url := stringValue(m["image_url"])
				if url == "" {
					if s := stringValue(m["file_id"]); s != "" {
						url = s
					}
				}
				if url != "" {
					img := map[string]any{"url": url}
					if detail := stringValue(m["detail"]); detail != "" {
						img["detail"] = detail
					}
					parts = append(parts, map[string]any{"type": "image_url", "image_url": img})
				}
			}
		}
		if len(parts) == 0 {
			return ""
		}
		if len(parts) == 1 {
			if p, ok := parts[0].(map[string]any); ok && p["type"] == "text" {
				return p["text"]
			}
		}
		return parts
	default:
		return ""
	}
}

func mergeAdjacentToolCalls(messages []any) []any {
	out := make([]any, 0, len(messages))
	for _, raw := range messages {
		m, ok := raw.(map[string]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		calls, hasCalls := m["tool_calls"].([]any)
		if !hasCalls || len(calls) == 0 || len(out) == 0 {
			out = append(out, m)
			continue
		}
		prev, ok := out[len(out)-1].(map[string]any)
		if !ok || prev["role"] != "assistant" {
			out = append(out, m)
			continue
		}
		prevCalls, _ := prev["tool_calls"].([]any)
		prev["tool_calls"] = append(prevCalls, calls...)
	}
	return out
}

func copyResponsesParams(src, dst map[string]any) {
	for _, k := range []string{"temperature", "top_p", "max_tokens", "metadata"} {
		if v, ok := src[k]; ok {
			dst[k] = v
		}
	}
	if v, ok := src["max_output_tokens"]; ok {
		dst["max_tokens"] = v
	}
	if v, ok := src["stop"]; ok {
		dst["stop"] = v
	}
	if v, ok := src["parallel_tool_calls"]; ok {
		dst["parallel_tool_calls"] = v
	}
	if v, ok := src["tools"]; ok {
		dst["tools"] = responsesToolsToChat(v)
	}
	if v, ok := src["tool_choice"]; ok {
		dst["tool_choice"] = responsesToolChoiceToChat(v)
	}
}

func responsesToolsToChat(v any) any {
	raw, ok := v.([]any)
	if !ok {
		return v
	}
	out := []any{}
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		typ := stringValue(m["type"])
		if typ == "" || typ == "function" {
			if fn, ok := m["function"].(map[string]any); ok {
				out = append(out, map[string]any{"type": "function", "function": fn})
				continue
			}
			fn := map[string]any{"name": stringValue(m["name"])}
			for _, k := range []string{"description", "parameters", "strict"} {
				if val, ok := m[k]; ok {
					fn[k] = val
				}
			}
			out = append(out, map[string]any{"type": "function", "function": fn})
			continue
		}
		// Chat Completions has no native custom/freeform tool type. Present the
		// same name as a one-string function so the upstream can still call it.
		if typ == "custom" {
			name := stringValue(m["name"])
			out = append(out, map[string]any{"type": "function", "function": map[string]any{
				"name":        name,
				"description": stringValue(m["description"]),
				"parameters": map[string]any{
					"type": "object", "properties": map[string]any{
						"input": map[string]any{"type": "string"},
					}, "required": []any{"input"},
				},
			}})
		}
	}
	return out
}

func responsesToolChoiceToChat(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	typ := stringValue(m["type"])
	switch typ {
	case "function":
		if name := stringValue(m["name"]); name != "" {
			return map[string]any{"type": "function", "function": map[string]any{"name": name}}
		}
	case "custom":
		if name := stringValue(m["name"]); name != "" {
			return map[string]any{"type": "function", "function": map[string]any{"name": name}}
		}
	case "allowed_tools":
		mode := stringValue(m["mode"])
		if mode == "required" {
			return "required"
		}
		return "auto"
	}
	return v
}

func chatToResponse(chat map[string]any, model string) map[string]any {
	choices, _ := chat["choices"].([]any)
	var msg map[string]any
	finish := ""
	if len(choices) > 0 {
		if c, ok := choices[0].(map[string]any); ok {
			msg, _ = c["message"].(map[string]any)
			finish, _ = c["finish_reason"].(string)
		}
	}
	text := ""
	if msg != nil {
		text = contentText(msg["content"])
	}
	output := []any{}
	if text != "" {
		output = append(output, map[string]any{
			"type": "message", "id": newID("msg_"), "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
		})
	}
	if msg != nil {
		output = append(output, chatToolCallsToResponses(msg["tool_calls"], newID("fc_"))...)
	}
	status := "completed"
	if finish == "length" {
		status = "incomplete"
	}
	usage := map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
	if u := usageFromChat(chat); u != nil {
		in := toInt64(u["prompt_tokens"])
		out := toInt64(u["completion_tokens"])
		usage = map[string]any{"input_tokens": in, "output_tokens": out, "total_tokens": in + out}
		if d, ok := u["input_tokens_details"]; ok {
			usage["input_tokens_details"] = d
		}
		if d, ok := u["output_tokens_details"]; ok {
			usage["output_tokens_details"] = d
		}
	}
	id := stringValue(chat["id"])
	if !strings.HasPrefix(id, "resp_") {
		id = newID("resp_")
	}
	result := map[string]any{
		"id": id, "object": "response", "created_at": time.Now().Unix(), "status": status,
		"model": model, "output": output, "parallel_tool_calls": true,
		"tool_choice": "auto", "tools": []any{}, "usage": usage, "error": nil,
	}
	if status == "incomplete" {
		result["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	}
	return result
}

func chatToolCallsToResponses(v any, idPrefix string) []any {
	calls, _ := v.([]any)
	out := make([]any, 0, len(calls))
	for _, raw := range calls {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		fn, _ := m["function"].(map[string]any)
		if fn == nil {
			continue
		}
		out = append(out, map[string]any{
			"id": idPrefix + stringValue(m["id"]), "type": "function_call",
			"call_id": stringValue(m["id"]), "name": stringValue(fn["name"]),
			"arguments": stringValue(fn["arguments"]), "status": "completed",
		})
	}
	return out
}

type toolCallAccumulator struct {
	order []int
	calls map[int]map[string]any
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{calls: map[int]map[string]any{}}
}

func (a *toolCallAccumulator) add(delta map[string]any) {
	raw, _ := delta["tool_calls"].([]any)
	for position, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		idx := position
		if v, ok := m["index"]; ok {
			idx = int(toInt64(v))
		}
		call, exists := a.calls[idx]
		if !exists {
			call = map[string]any{"id": "", "type": "function", "function": map[string]any{"name": "", "arguments": ""}}
			a.calls[idx] = call
			a.order = append(a.order, idx)
		}
		if s := stringValue(m["id"]); s != "" {
			call["id"] = s
		}
		fn, _ := m["function"].(map[string]any)
		if fn == nil {
			continue
		}
		if s := stringValue(fn["name"]); s != "" {
			call["function"].(map[string]any)["name"] = s
		}
		if s := stringValue(fn["arguments"]); s != "" {
			current := stringValue(call["function"].(map[string]any)["arguments"])
			call["function"].(map[string]any)["arguments"] = current + s
		}
	}
}

func (a *toolCallAccumulator) list() []any {
	out := make([]any, 0, len(a.order))
	for _, idx := range a.order {
		call := a.calls[idx]
		if stringValue(call["id"]) == "" {
			call["id"] = newID("call_")
		}
		if fn, ok := call["function"].(map[string]any); ok && stringValue(fn["name"]) == "" {
			continue
		}
		out = append(out, call)
	}
	return out
}

func streamResponses(w http.ResponseWriter, body io.Reader, model string) {
	setSSE(w)
	seq := 0
	id := newID("resp_")
	itemID := newID("msg_")
	sentStart := false
	var text strings.Builder
	var usage map[string]any
	finish := ""
	toolCalls := newToolCallAccumulator()
	writeEvent := func(event string, obj map[string]any) error {
		obj["sequence_number"] = seq
		seq++
		raw, _ := json.Marshal(obj)
		return sseWrite(w, event, string(raw))
	}
	ensureStart := func() error {
		if sentStart {
			return nil
		}
		sentStart = true
		if err := writeEvent("response.created", map[string]any{
			"type": "response.created", "response": map[string]any{
				"id": id, "object": "response", "created_at": time.Now().Unix(),
				"status": "in_progress", "model": model, "output": []any{},
			},
		}); err != nil {
			return err
		}
		return writeEvent("response.output_item.added", map[string]any{
			"type": "response.output_item.added", "output_index": 0,
			"item": map[string]any{"id": itemID, "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}},
		})
	}
	err := parseSSE(body, func(_ string, data string) error {
		if data == "[DONE]" {
			return nil
		}
		var chunk map[string]any
		dec := json.NewDecoder(strings.NewReader(data))
		dec.UseNumber()
		if err := dec.Decode(&chunk); err != nil {
			return nil
		}
		if err := ensureStart(); err != nil {
			return err
		}
		if u := usageFromChat(chunk); u != nil {
			usage = u
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) > 0 {
			c, _ := choices[0].(map[string]any)
			if d, ok := c["delta"].(map[string]any); ok {
				if t, ok := d["content"].(string); ok && t != "" {
					text.WriteString(t)
					if err := writeEvent("response.output_text.delta", map[string]any{
						"type": "response.output_text.delta", "item_id": itemID,
						"output_index": 0, "content_index": 0, "delta": t,
					}); err != nil {
						return err
					}
				}
				toolCalls.add(d)
			}
			if fr, ok := c["finish_reason"].(string); ok && fr != "" {
				finish = fr
			}
		}
		return nil
	})
	if err != nil {
		return
	}
	if err := ensureStart(); err != nil {
		return
	}
	_ = writeEvent("response.output_text.done", map[string]any{
		"type": "response.output_text.done", "item_id": itemID,
		"output_index": 0, "content_index": 0, "text": text.String(),
	})
	_ = writeEvent("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "output_index": 0,
		"item": map[string]any{
			"id": itemID, "type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": text.String(), "annotations": []any{}}},
		},
	})
	output := []any{map[string]any{
		"id": itemID, "type": "message", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": text.String(), "annotations": []any{}}},
	}}
	for i, tc := range toolCalls.list() {
		call := tc.(map[string]any)
		fn := call["function"].(map[string]any)
		outputIndex := i + 1
		fcItemID := newID("fc_")
		args := stringValue(fn["arguments"])
		_ = writeEvent("response.output_item.added", map[string]any{
			"type": "response.output_item.added", "output_index": outputIndex,
			"item": map[string]any{
				"id": fcItemID, "type": "function_call", "call_id": stringValue(call["id"]),
				"name": stringValue(fn["name"]), "arguments": "", "status": "in_progress",
			},
		})
		_ = writeEvent("response.function_call_arguments.delta", map[string]any{
			"type": "response.function_call_arguments.delta", "item_id": fcItemID,
			"output_index": outputIndex, "delta": args,
		})
		_ = writeEvent("response.function_call_arguments.done", map[string]any{
			"type": "response.function_call_arguments.done", "item_id": fcItemID,
			"output_index": outputIndex, "arguments": args,
		})
		fc := map[string]any{
			"id": fcItemID, "type": "function_call", "call_id": stringValue(call["id"]),
			"name": stringValue(fn["name"]), "arguments": args, "status": "completed",
		}
		_ = writeEvent("response.output_item.done", map[string]any{
			"type": "response.output_item.done", "output_index": outputIndex, "item": fc,
		})
		output = append(output, fc)
	}
	in, out := int64(0), int64(0)
	if usage != nil {
		in = toInt64(usage["prompt_tokens"])
		out = toInt64(usage["completion_tokens"])
	}
	status := "completed"
	if finish == "length" {
		status = "incomplete"
	}
	_ = writeEvent("response.completed", map[string]any{
		"type": "response.completed", "response": map[string]any{
			"id": id, "object": "response", "created_at": time.Now().Unix(), "status": status,
			"model": model, "output": output,
			"usage": map[string]any{"input_tokens": in, "output_tokens": out, "total_tokens": in + out},
		},
	})
}

func forwardAsResponsesError(w http.ResponseWriter, status int, body []byte) {
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&obj); err == nil {
		if e, ok := obj["error"].(map[string]any); ok {
			writeJSON(w, status, map[string]any{"error": e})
			return
		}
	}
	writeJSON(w, status, errorEnvelope("upstream_error", http.StatusText(status)))
}

func errorEnvelope(typ, msg string) map[string]any {
	return map[string]any{"error": map[string]any{"type": typ, "message": msg}}
}

func stringValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return fmt.Sprintf("%v", x)
	case int:
		return fmt.Sprintf("%d", x)
	case int64:
		return fmt.Sprintf("%d", x)
	case bool:
		return fmt.Sprintf("%t", x)
	default:
		return ""
	}
}
