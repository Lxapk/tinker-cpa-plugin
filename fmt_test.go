package main

import (
	"encoding/json"
	"testing"
)

// TestStreamChunksAreSingleJSONObjects reproduces what the client does: split the
// stream on newlines, take each "data:" line, and json.Unmarshal it. With
// pre-framed chunks the second colon broke exactly like the reported error.
func TestStreamChunksAreSingleJSONObjects(t *testing.T) {
	frames := buildSSEFrames("chatcmpl-1", "gpt-5.2", "你好世界")
	for i, f := range frames {
		var probe map[string]any
		if err := json.Unmarshal(f.Payload, &probe); err != nil {
			t.Fatalf("第 %d 帧不是单一 JSON 对象: %v\n原文: %s", i, err, f.Payload)
		}
	}
	// Simulate CPA's framing, then parse the way a client would.
	var wire []byte
	for _, f := range frames {
		wire = append(wire, []byte("data: ")...)
		wire = append(wire, f.Payload...)
		wire = append(wire, '\n', '\n')
	}
	wire = append(wire, []byte("data: [DONE]\n\n")...)

	count := 0
	lineStart := 0
	for i := 0; i <= len(wire); i++ {
		if i == len(wire) || wire[i] == '\n' {
			line := string(wire[lineStart:i])
			lineStart = i + 1
			if len(line) <= 6 || line[:6] != "data: " {
				continue
			}
			payload := line[6:]
			if payload == "[DONE]" {
				count++
				continue
			}
			var probe map[string]any
			if err := json.Unmarshal([]byte(payload), &probe); err != nil {
				t.Fatalf("客户端解析失败: %v\n行: %s", err, line)
			}
			count++
		}
	}
	t.Logf("客户端成功解析 %d 行", count)
}
