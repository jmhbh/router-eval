package parsers

import (
	"testing"

	"router-eval/internal/artifacts"
)

func TestParseChatCompletionUsage(t *testing.T) {
	result := Parse("/v1/chat/completions", "application/json", []byte(`{
	  "id": "chatcmpl-123",
	  "usage": {
	    "prompt_tokens": 10,
	    "completion_tokens": 5,
	    "total_tokens": 15,
	    "cost": 0.0012
	  }
	}`))

	if result.ID != "chatcmpl-123" {
		t.Fatalf("id=%q", result.ID)
	}
	if result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 5 {
		t.Fatalf("usage=%+v", result.Usage)
	}
	if result.Usage.CostState != artifacts.CostStateInlineConfirmed {
		t.Fatalf("cost state=%s", result.Usage.CostState)
	}
}

func TestParseResponsesSSEUsage(t *testing.T) {
	body := []byte("event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"gen-1\",\"usage\":{\"input_tokens\":20,\"output_tokens\":30,\"total_tokens\":50,\"cost\":\"0.004\"}}}\n\n")

	result := Parse("/v1/responses", "text/event-stream", body)
	if result.ID != "gen-1" {
		t.Fatalf("id=%q", result.ID)
	}
	if result.Usage.InputTokens != 20 || result.Usage.OutputTokens != 30 || result.Usage.CostUSD != 0.004 {
		t.Fatalf("usage=%+v", result.Usage)
	}
}

func TestParseChatCompletionToolCalls(t *testing.T) {
	result := Parse("/v1/chat/completions", "application/json", []byte(`{
	  "id": "chatcmpl-123",
	  "choices": [{
	    "message": {
	      "tool_calls": [{
	        "id": "call_1",
	        "type": "function",
	        "function": {
	          "name": "lookup",
	          "arguments": "{\"query\":\"router\"}"
	        }
	      }]
	    }
	  }]
	}`))

	if result.ToolCalls.Count != 1 || result.ToolCalls.ValidCount != 1 {
		t.Fatalf("tool calls=%+v", result.ToolCalls)
	}
	if len(result.ToolDetails) != 1 || result.ToolDetails[0].Name != "lookup" || result.ToolDetails[0].Arguments == "" {
		t.Fatalf("tool details=%+v", result.ToolDetails)
	}
}

func TestParseResponsesSSEToolCalls(t *testing.T) {
	body := []byte("event: response.output_item.done\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"name\":\"lookup\",\"arguments\":\"{\\\"query\\\":\\\"router\\\"}\"}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"id\":\"fc_1\",\"type\":\"function_call\",\"name\":\"lookup\",\"arguments\":\"{\\\"query\\\":\\\"router\\\"}\"}]}}\n\n")

	result := Parse("/v1/responses", "text/event-stream", body)
	if result.ToolCalls.Count != 1 || result.ToolCalls.ValidCount != 1 {
		t.Fatalf("tool calls=%+v", result.ToolCalls)
	}
	if len(result.ToolDetails) != 1 || result.ToolDetails[0].ID != "fc_1" || result.ToolDetails[0].Name != "lookup" {
		t.Fatalf("tool details=%+v", result.ToolDetails)
	}
}

func TestParseResponsesSSEIgnoresIncompleteToolCallAdded(t *testing.T) {
	body := []byte("event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"status\":\"in_progress\",\"arguments\":\"\",\"call_id\":\"call_1\",\"name\":\"exec_command\"},\"output_index\":1,\"sequence_number\":4}\n\n" +
		"event: response.function_call_arguments.delta\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\\\"cmd\\\":\",\"item_id\":\"fc_1\",\"output_index\":1,\"sequence_number\":5}\n\n" +
		"event: response.function_call_arguments.done\n" +
		"data: {\"type\":\"response.function_call_arguments.done\",\"arguments\":\"{\\\"cmd\\\":\\\"make test\\\"}\",\"item_id\":\"fc_1\",\"output_index\":1,\"sequence_number\":6}\n\n" +
		"event: response.output_item.done\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"status\":\"completed\",\"arguments\":\"{\\\"cmd\\\":\\\"make test\\\"}\",\"call_id\":\"call_1\",\"name\":\"exec_command\"},\"output_index\":1,\"sequence_number\":7}\n\n")

	result := Parse("/v1/responses", "text/event-stream", body)
	if result.ToolCalls.Count != 1 || result.ToolCalls.ValidCount != 1 {
		t.Fatalf("tool calls=%+v details=%+v", result.ToolCalls, result.ToolDetails)
	}
	if len(result.ToolDetails) != 1 || result.ToolDetails[0].Arguments != `{"cmd":"make test"}` || !result.ToolDetails[0].ArgumentsValid {
		t.Fatalf("tool details=%+v", result.ToolDetails)
	}
}
