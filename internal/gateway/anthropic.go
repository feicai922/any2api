package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"api-gateway/internal/config"
)

func (g *Gateway) anthropicFromChat(w http.ResponseWriter, r *http.Request, project config.ResolvedProject) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": map[string]any{"type": "invalid_request_error", "message": "method not allowed"}})
		return
	}
	obj, _, err := readJSON(r, g.maxBodyBytes)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON: "+err.Error())
		return
	}
	if strings.HasSuffix(r.URL.Path, "/count_tokens") {
		g.countAnthropicTokens(w, obj)
		return
	}
	messages, err := anthropicMessages(obj)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	stream := isStream(r, obj)
	chat := map[string]any{
		"model": modelName(obj), "messages": messages, "stream": stream,
	}
	for _, k := range []string{"temperature", "top_p", "top_k"} {
		if v, ok := obj[k]; ok {
			chat[k] = v
		}
	}
	if stream {
		chat["stream_options"] = map[string]any{"include_usage": true}
	}
	for _, k := range []string{"max_tokens", "metadata"} {
		if v, ok := obj[k]; ok {
			chat[k] = v
		}
	}
	if v, ok := obj["stop_sequences"]; ok {
		chat["stop"] = v
	}
	if v, ok := obj["tools"]; ok {
		chat["tools"] = anthropicToolsToChat(v)
	}
	if v, ok := obj["tool_choice"]; ok {
		chat["tool_choice"] = anthropicToolChoiceToChat(v)
		if tc, ok := v.(map[string]any); ok {
			if disable, _ := tc["disable_parallel_tool_use"].(bool); disable {
				chat["parallel_tool_calls"] = false
			}
		}
	}
	extra := http.Header{}
	copyClientAuth(r, extra)
	resp, err := g.sendChat(r, project, chat, extra)
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "upstream unavailable: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		writeAnthropicUpstreamError(w, resp.StatusCode, body)
		return
	}
	if stream {
		streamAnthropic(w, resp.Body, modelName(obj))
		return
	}
	var chatResp map[string]any
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(&chatResp); err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "invalid upstream response")
		return
	}
	writeJSON(w, http.StatusOK, chatToAnthropic(chatResp, modelName(obj)))
}

func anthropicMessages(obj map[string]any) ([]any, error) {
	out := []any{}
	if sys := obj["system"]; sys != nil {
		if s := contentText(sys); s != "" {
			out = append(out, map[string]any{"role": "system", "content": s})
		}
	}
	raw, ok := obj["messages"].([]any)
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("messages must be a non-empty array")
	}
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if role == "" {
			continue
		}
		text, toolCalls, toolResults := anthropicContentToChat(m["content"])
		if len(toolResults) > 0 {
			out = append(out, toolResults...)
		}
		if text == nil && len(toolCalls) == 0 {
			continue
		}
		msg := map[string]any{"role": role, "content": text}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		out = append(out, msg)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("messages must include text or tool content")
	}
	return out, nil
}

// anthropicContentToChat returns text, assistant tool calls and tool results.
// Anthropic permits tool_result blocks inside a user message; Chat Completions
// represents each result as its own role=tool message.
func anthropicContentToChat(v any) (any, []any, []any) {
	switch x := v.(type) {
	case string:
		return x, nil, nil
	case []any:
		var text strings.Builder
		var parts []any
		var calls []any
		var results []any
		for _, raw := range x {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch m["type"] {
			case "text":
				t, _ := m["text"].(string)
				text.WriteString(t)
				if t != "" {
					parts = append(parts, map[string]any{"type": "text", "text": t})
				}
			case "image":
				source, _ := m["source"].(map[string]any)
				if source != nil {
					if data, ok := source["data"].(string); ok {
						mt, _ := source["media_type"].(string)
						if mt == "" {
							mt = "image/png"
						}
						parts = append(parts, map[string]any{
							"type": "image_url", "image_url": map[string]any{"url": "data:" + mt + ";base64," + data},
						})
					}
				}
			case "tool_use":
				input, _ := json.Marshal(m["input"])
				calls = append(calls, map[string]any{
					"id": stringValue(m["id"]), "type": "function",
					"function": map[string]any{"name": stringValue(m["name"]), "arguments": string(input)},
				})
			case "tool_result":
				content := m["content"]
				results = append(results, map[string]any{
					"role": "tool", "tool_call_id": stringValue(m["tool_use_id"]),
					"content": contentText(content),
				})
			}
		}
		// Chat Completions accepts structured text/image parts. If there are
		// only text pieces, keep the simpler string form for broad upstream
		// compatibility.
		if len(parts) == 0 || len(calls) > 0 || len(results) > 0 {
			return text.String(), calls, results
		}
		hasImage := false
		for _, p := range parts {
			if p.(map[string]any)["type"] == "image_url" {
				hasImage = true
			}
		}
		if !hasImage {
			return text.String(), nil, nil
		}
		return parts, nil, nil
	default:
		return "", nil, nil
	}
}

func anthropicToolsToChat(v any) any {
	raw, ok := v.([]any)
	if !ok {
		return v
	}
	out := []any{}
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fn := map[string]any{"name": stringValue(m["name"])}
		if d := stringValue(m["description"]); d != "" {
			fn["description"] = d
		}
		if schema, ok := m["input_schema"]; ok {
			fn["parameters"] = schema
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out
}

func anthropicToolChoiceToChat(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	switch stringValue(m["type"]) {
	case "any":
		return "required"
	case "tool":
		if name := stringValue(m["name"]); name != "" {
			return map[string]any{"type": "function", "function": map[string]any{"name": name}}
		}
		return "required"
	case "none":
		return "none"
	default:
		return "auto"
	}
}

func chatToAnthropic(chat map[string]any, model string) map[string]any {
	choices, _ := chat["choices"].([]any)
	text := ""
	stop := "end_turn"
	content := []any{}
	var toolCalls any
	if len(choices) > 0 {
		c, _ := choices[0].(map[string]any)
		if msg, ok := c["message"].(map[string]any); ok {
			text = contentText(msg["content"])
			toolCalls = msg["tool_calls"]
		}
		if fr, _ := c["finish_reason"].(string); fr == "length" {
			stop = "max_tokens"
		} else if fr == "tool_calls" {
			stop = "tool_use"
		}
	}
	if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	content = append(content, chatToolCallsToAnthropic(toolCalls)...)
	if len(content) == 0 {
		content = append(content, map[string]any{"type": "text", "text": ""})
	}
	usage := map[string]any{"input_tokens": 0, "output_tokens": 0}
	if u := usageFromChat(chat); u != nil {
		usage = map[string]any{
			"input_tokens":  toInt64(u["prompt_tokens"]),
			"output_tokens": toInt64(u["completion_tokens"]),
		}
	}
	id := stringValue(chat["id"])
	if id == "" {
		id = newID("msg_")
	}
	return map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": model,
		"content": content, "stop_reason": stop, "stop_sequence": nil, "usage": usage,
	}
}

func chatToolCallsToAnthropic(v any) []any {
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
		var input any = map[string]any{}
		args := stringValue(fn["arguments"])
		if args != "" {
			if err := json.Unmarshal([]byte(args), &input); err != nil {
				input = map[string]any{"input": args}
			}
		}
		out = append(out, map[string]any{
			"type": "tool_use", "id": stringValue(m["id"]),
			"name": stringValue(fn["name"]), "input": input,
		})
	}
	return out
}

func streamAnthropic(w http.ResponseWriter, body io.Reader, model string) {
	setSSE(w)
	msgID := newID("msg_")
	started := false
	textStarted := false
	var text strings.Builder
	var usage map[string]any
	finish := ""
	toolCalls := newToolCallAccumulator()
	send := func(event string, obj map[string]any) error {
		raw, _ := json.Marshal(obj)
		return sseWrite(w, event, string(raw))
	}
	ensureStart := func() error {
		if started {
			return nil
		}
		started = true
		return send("message_start", map[string]any{
			"type": "message_start", "message": map[string]any{
				"id": msgID, "type": "message", "role": "assistant", "model": model,
				"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
			},
		})
	}
	ensureText := func() error {
		if textStarted {
			return nil
		}
		textStarted = true
		return send("content_block_start", map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
	}
	_ = parseSSE(body, func(_ string, data string) error {
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
					if err := ensureText(); err != nil {
						return err
					}
					text.WriteString(t)
					if err := send("content_block_delta", map[string]any{
						"type": "content_block_delta", "index": 0,
						"delta": map[string]any{"type": "text_delta", "text": t},
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
	if err := ensureStart(); err != nil {
		return
	}
	if textStarted {
		_ = send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	}
	blockIndex := 0
	if textStarted {
		blockIndex = 1
	}
	for _, tc := range toolCalls.list() {
		call := tc.(map[string]any)
		fn := call["function"].(map[string]any)
		args := stringValue(fn["arguments"])
		_ = send("content_block_start", map[string]any{
			"type": "content_block_start", "index": blockIndex,
			"content_block": map[string]any{
				"type": "tool_use", "id": stringValue(call["id"]),
				"name": stringValue(fn["name"]), "input": map[string]any{},
			},
		})
		_ = send("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": blockIndex,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": args},
		})
		_ = send("content_block_stop", map[string]any{"type": "content_block_stop", "index": blockIndex})
		blockIndex++
		finish = "tool_calls"
	}
	stop := "end_turn"
	switch finish {
	case "length":
		stop = "max_tokens"
	case "tool_calls":
		stop = "tool_use"
	}
	in, out := int64(0), int64(0)
	if usage != nil {
		in = toInt64(usage["prompt_tokens"])
		out = toInt64(usage["completion_tokens"])
	}
	_ = send("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
		"usage": map[string]any{"input_tokens": in, "output_tokens": out},
	})
	_ = send("message_stop", map[string]any{"type": "message_stop"})
}

func (g *Gateway) countAnthropicTokens(w http.ResponseWriter, obj map[string]any) {
	msgs, _ := anthropicMessages(obj)
	chars := 0
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		chars += len(contentText(m["content"]))
		if calls, ok := m["tool_calls"].([]any); ok {
			for _, call := range calls {
				if cm, ok := call.(map[string]any); ok {
					chars += len(stringValue(cm["function"]))
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"input_tokens": maxInt(1, (chars+3)/4)})
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func writeAnthropicError(w http.ResponseWriter, status int, typ, msg string) {
	writeJSON(w, status, map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": msg}})
}

func writeAnthropicUpstreamError(w http.ResponseWriter, status int, body []byte) {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err == nil {
		if e, ok := obj["error"].(map[string]any); ok {
			writeJSON(w, status, map[string]any{"type": "error", "error": e})
			return
		}
	}
	writeAnthropicError(w, status, "api_error", http.StatusText(status))
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
