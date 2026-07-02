package action

import (
	"testing"
	"time"

	"github.com/Forest-Isle/daimon/internal/tool"
	"github.com/Forest-Isle/daimon/internal/world"
)

func declaredCaps(class string) tool.ToolCapabilities {
	return tool.ToolCapabilities{Reversibility: class}
}

func reversibleCaps() tool.ToolCapabilities {
	return declaredCaps("reversible")
}

func TestClassifierClassifiesDeclaredBuiltinTools(t *testing.T) {
	classifiers := []struct {
		name string
		c    Classifier
	}{
		{name: "default", c: NewClassifier()},
		{name: "hold-aware", c: NewClassifierWithCompensableHTTP()},
	}

	tests := []struct {
		name       string
		call       *tool.ToolCall
		wantClass  Class
		wantGovern bool
	}{
		{
			name: "file_write",
			call: &tool.ToolCall{
				ToolName:     "file_write",
				Input:        `{"path":"x","content":"y"}`,
				Capabilities: tool.GetCapabilities(tool.NewFileWriteTool(false)),
			},
			wantClass:  Reversible,
			wantGovern: true,
		},
		{
			name: "file_edit",
			call: &tool.ToolCall{
				ToolName:     "file_edit",
				Input:        `{"path":"x","old_string":"a","new_string":"b"}`,
				Capabilities: tool.GetCapabilities(tool.NewFileEditTool(false)),
			},
			wantClass:  Reversible,
			wantGovern: true,
		},
		{
			name: "file_patch",
			call: &tool.ToolCall{
				ToolName:     "file_patch",
				Input:        `{"path":"x","patch":"@@ -1 +1 @@\n-a\n+b"}`,
				Capabilities: tool.GetCapabilities(tool.NewFilePatchTool(".")),
			},
			wantClass:  Reversible,
			wantGovern: true,
		},
		{
			name: "world_edit",
			call: &tool.ToolCall{
				ToolName:     "world_edit",
				Input:        `{"file":"profile.md","content":"x"}`,
				Capabilities: tool.GetCapabilities(tool.NewWorldEditTool(world.Identity{})),
			},
			wantClass:  Reversible,
			wantGovern: true,
		},
		{
			name: "commitment",
			call: &tool.ToolCall{
				ToolName:     "commitment",
				Input:        `{"action":"create","kind":"project","title":"ship"}`,
				Capabilities: tool.GetCapabilities(tool.NewCommitmentTool(nil)),
			},
			wantClass:  Reversible,
			wantGovern: true,
		},
		{
			name: "send_email",
			call: &tool.ToolCall{
				ToolName:     "send_email",
				Input:        `{"to":"user@example.com","subject":"Hello","body":"Body text"}`,
				Capabilities: tool.GetCapabilities(tool.NewSendEmailTool("smtp.example.com", 587, "agent@example.com", "password", "", false)),
			},
			wantClass:  Compensable,
			wantGovern: true,
		},
		{
			name: "memory_save",
			call: &tool.ToolCall{
				ToolName:     "memory",
				Input:        `{"operation":"save","content":"x"}`,
				Capabilities: tool.GetCapabilities(tool.NewMemoryTool(nil, nil)),
			},
			wantClass:  Irreversible,
			wantGovern: true,
		},
		{
			name: "values_record",
			call: &tool.ToolCall{
				ToolName:     "values",
				Input:        `{"operation":"record","content":"never self-authorize"}`,
				Capabilities: tool.GetCapabilities(tool.NewValuesTool(nil)),
			},
			wantClass:  Irreversible,
			wantGovern: true,
		},
		{
			name: "bash_readish",
			call: &tool.ToolCall{
				ToolName:     "bash",
				Input:        `{"command":"ls -la"}`,
				Capabilities: tool.GetCapabilities(tool.NewBashTool(time.Second, false, tool.NewPolicy(nil))),
			},
			wantClass:  Reversible,
			wantGovern: true,
		},
		{
			name: "bash_destructive",
			call: &tool.ToolCall{
				ToolName:     "bash",
				Input:        `{"command":"rm -rf build"}`,
				Capabilities: tool.GetCapabilities(tool.NewBashTool(time.Second, false, tool.NewPolicy(nil))),
			},
			wantClass:  Irreversible,
			wantGovern: true,
		},
		{
			name: "http_post",
			call: &tool.ToolCall{
				ToolName:     "http",
				Input:        `{"method":"POST","url":"https://example.com"}`,
				Capabilities: tool.GetCapabilities(tool.NewHTTPTool(time.Second, false)),
			},
			wantClass:  Compensable,
			wantGovern: true,
		},
		{
			name: "http_get_fail_closed",
			call: &tool.ToolCall{
				ToolName:     "http",
				Input:        `{"method":"GET","url":"https://example.com"}`,
				Capabilities: tool.GetCapabilities(tool.NewHTTPTool(time.Second, false)),
			},
			wantClass:  Irreversible,
			wantGovern: true,
		},
	}

	for _, classifier := range classifiers {
		for _, tt := range tests {
			t.Run(classifier.name+"/"+tt.name, func(t *testing.T) {
				class, governed := classifier.c.Classify(tt.call)
				if class != tt.wantClass || governed != tt.wantGovern {
					t.Fatalf("Classify() = (%v, %v), want (%v, %v)", class, governed, tt.wantClass, tt.wantGovern)
				}
			})
		}
	}
}

func TestClassifierReadOnlyUngoverned(t *testing.T) {
	classifiers := []Classifier{NewClassifier(), NewClassifierWithCompensableHTTP()}
	for _, c := range classifiers {
		class, governed := c.Classify(&tool.ToolCall{
			ToolName:     "world_read",
			Capabilities: tool.ToolCapabilities{IsReadOnly: true},
		})
		if class != Reversible || governed {
			t.Fatalf("Classify() = (%v, %v), want Reversible/false", class, governed)
		}
	}
}

func TestClassifierFailClosedForUndeclaredMutatingTool(t *testing.T) {
	classifiers := []Classifier{NewClassifier(), NewClassifierWithCompensableHTTP()}
	for _, c := range classifiers {
		class, governed := c.Classify(&tool.ToolCall{ToolName: "new_mutator"})
		if class != Irreversible || !governed {
			t.Fatalf("undeclared mutating tool classified (%v, %v), want Irreversible/true", class, governed)
		}
	}
}

func TestClassifierFailClosedForInvalidReversibility(t *testing.T) {
	classifiers := []Classifier{NewClassifier(), NewClassifierWithCompensableHTTP()}
	for _, c := range classifiers {
		class, governed := c.Classify(&tool.ToolCall{
			ToolName:     "bad_tool",
			Capabilities: declaredCaps("undoable"),
		})
		if class != Irreversible || !governed {
			t.Fatalf("invalid declaration classified (%v, %v), want Irreversible/true", class, governed)
		}
	}
}

func TestHoldAwareClassifierReadOperationsUngoverned(t *testing.T) {
	c := NewClassifierWithCompensableHTTP()
	cases := []struct {
		name string
		call *tool.ToolCall
	}{
		{
			name: "memory_search",
			call: &tool.ToolCall{
				ToolName:     "memory",
				Input:        `{"operation":"search"}`,
				Capabilities: declaredCaps("irreversible"),
			},
		},
		{
			name: "memory_list",
			call: &tool.ToolCall{
				ToolName:     "memory",
				Input:        `{"operation":"list"}`,
				Capabilities: declaredCaps("irreversible"),
			},
		},
		{
			name: "values_list",
			call: &tool.ToolCall{
				ToolName:     "values",
				Input:        `{"operation":"list"}`,
				Capabilities: declaredCaps("irreversible"),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			class, governed := c.Classify(tc.call)
			if class != Reversible || governed {
				t.Fatalf("Classify() = (%v, %v), want Reversible/false", class, governed)
			}
		})
	}
}
