package openai

import (
	"encoding/json"
	"testing"
)

func TestExtractTextToolCall_plainJSON(t *testing.T) {
	input := `{"name":"read_file","arguments":{"path":"main.go"}}`
	name, argsJSON, ok := extractTextToolCall(input)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if name != "read_file" {
		t.Errorf("name: want read_file, got %q", name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		t.Fatalf("args not valid JSON: %v", err)
	}
	if args["path"] != "main.go" {
		t.Errorf("args.path: want main.go, got %v", args["path"])
	}
}

func TestExtractTextToolCall_embeddedInProse(t *testing.T) {
	input := "Let me list the files for you:\n{\"name\":\"list_files\",\"arguments\":{\"path\":\".\"}}"
	name, _, ok := extractTextToolCall(input)
	if !ok {
		t.Fatal("expected ok=true for JSON embedded in prose")
	}
	if name != "list_files" {
		t.Errorf("name: want list_files, got %q", name)
	}
}

func TestExtractTextToolCall_inCodeFence(t *testing.T) {
	input := "```json\n{\"name\":\"write_file\",\"arguments\":{\"path\":\"out.txt\",\"content\":\"hello\"}}\n```"
	name, argsJSON, ok := extractTextToolCall(input)
	if !ok {
		t.Fatal("expected ok=true for JSON in code fence")
	}
	if name != "write_file" {
		t.Errorf("name: want write_file, got %q", name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		t.Fatalf("args not valid JSON: %v", err)
	}
	if args["path"] != "out.txt" {
		t.Errorf("args.path: want out.txt, got %v", args["path"])
	}
}

func TestExtractTextToolCall_parametersAlias(t *testing.T) {
	// some models use "parameters" instead of "arguments"
	input := `{"name":"run_command","parameters":{"command":"ls"}}`
	name, argsJSON, ok := extractTextToolCall(input)
	if !ok {
		t.Fatal("expected ok=true with parameters alias")
	}
	if name != "run_command" {
		t.Errorf("name: want run_command, got %q", name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		t.Fatalf("args not valid JSON: %v", err)
	}
	if args["command"] != "ls" {
		t.Errorf("args.command: want ls, got %v", args["command"])
	}
}

func TestExtractTextToolCall_noArguments(t *testing.T) {
	// no arguments/parameters field → returns empty object
	input := `{"name":"list_files"}`
	name, argsJSON, ok := extractTextToolCall(input)
	if !ok {
		t.Fatal("expected ok=true even with no arguments")
	}
	if name != "list_files" {
		t.Errorf("name: want list_files, got %q", name)
	}
	if argsJSON != "{}" {
		t.Errorf("argsJSON: want {}, got %q", argsJSON)
	}
}

func TestExtractTextToolCall_plainText(t *testing.T) {
	_, _, ok := extractTextToolCall("I will now read the file for you.")
	if ok {
		t.Error("expected ok=false for plain prose with no JSON")
	}
}

func TestExtractTextToolCall_unrelatedJSON(t *testing.T) {
	_, _, ok := extractTextToolCall(`{"status":"ok","code":200}`)
	if ok {
		t.Error("expected ok=false for JSON without 'name' field")
	}
}

func TestExtractTextToolCall_trailingGarbage(t *testing.T) {
	// Decoder should tolerate trailing bytes
	input := `{"name":"read_file","arguments":{"path":"x.go"}}   extra stuff`
	name, _, ok := extractTextToolCall(input)
	if !ok {
		t.Fatal("expected ok=true despite trailing garbage")
	}
	if name != "read_file" {
		t.Errorf("name: want read_file, got %q", name)
	}
}

func TestExtractTextToolCall_fenceNoLanguageTag(t *testing.T) {
	input := "```\n{\"name\":\"read_file\",\"arguments\":{\"path\":\"go.mod\"}}\n```"
	name, _, ok := extractTextToolCall(input)
	if !ok {
		t.Fatal("expected ok=true for fence without language tag")
	}
	if name != "read_file" {
		t.Errorf("name: want read_file, got %q", name)
	}
}
