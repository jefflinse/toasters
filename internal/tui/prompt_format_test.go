package tui

import (
	"testing"

	"github.com/jefflinse/toasters/internal/service"
)

func TestCombinePromptAnswers(t *testing.T) {
	tests := []struct {
		name    string
		round   []service.PromptQuestion
		answers []string
		want    string
	}{
		{
			name:    "single question returns bare answer (back-compat)",
			round:   []service.PromptQuestion{{Question: "Framework?"}},
			answers: []string{"net/http"},
			want:    "net/http",
		},
		{
			name: "multi question returns numbered Q-to-A block",
			round: []service.PromptQuestion{
				{Question: "Framework?"},
				{Question: "DB driver?"},
			},
			answers: []string{"net/http", "modernc"},
			want:    "1. Framework?\n   → net/http\n\n2. DB driver?\n   → modernc",
		},
		{
			name:    "empty answers yields empty string",
			round:   []service.PromptQuestion{{Question: "Framework?"}},
			answers: nil,
			want:    "",
		},
		{
			name: "missing answer renders an empty arrow",
			round: []service.PromptQuestion{
				{Question: "A?"},
				{Question: "B?"},
			},
			answers: []string{"yes"},
			want:    "1. A?\n   → yes\n\n2. B?\n   → ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := combinePromptAnswers(tt.round, tt.answers); got != tt.want {
				t.Errorf("combinePromptAnswers() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestPromptHistoryContent(t *testing.T) {
	tests := []struct {
		name  string
		round []service.PromptQuestion
		want  string
	}{
		{
			name:  "single question is shown plainly",
			round: []service.PromptQuestion{{Question: "Framework?"}},
			want:  "Framework?",
		},
		{
			name: "multi question is numbered",
			round: []service.PromptQuestion{
				{Question: "Framework?"},
				{Question: "DB driver?"},
			},
			want: "1. Framework?\n2. DB driver?",
		},
		{
			name:  "empty round yields empty string",
			round: nil,
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := promptHistoryContent(tt.round); got != tt.want {
				t.Errorf("promptHistoryContent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsSystemNode(t *testing.T) {
	if !isSystemNode("decompose") {
		t.Error("decompose node should be a system node")
	}
	if isSystemNode("plan") {
		t.Error("plan node is real work, not a system node")
	}
}

func TestIsSystemTask(t *testing.T) {
	cases := []struct {
		task service.Task
		want bool
	}{
		{service.Task{GraphID: "coarse-decompose"}, true},
		{service.Task{GraphID: "fine-decompose"}, true},
		{service.Task{Title: "Decompose: To-Do app"}, true},
		{service.Task{Title: "Pick graph: backend"}, true},
		{service.Task{GraphID: "new-feature", Title: "Go backend"}, false},
	}
	for _, c := range cases {
		if got := isSystemTask(c.task); got != c.want {
			t.Errorf("isSystemTask(%+v) = %v, want %v", c.task, got, c.want)
		}
	}
}
