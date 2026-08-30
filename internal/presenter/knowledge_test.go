package presenter

import (
	"testing"

	"github.com/red060324/XiaoLanHe/internal/usecase"
)

func TestKnowledgeDocumentRequestInput(t *testing.T) {
	for _, tt := range []struct {
		name    string
		request KnowledgeDocumentRequest
		wantErr bool
	}{{"valid", KnowledgeDocumentRequest{SourceType: "note", Title: "title", ContentText: "body"}, false}, {"source", KnowledgeDocumentRequest{Title: "title", ContentText: "body"}, true}, {"title", KnowledgeDocumentRequest{SourceType: "note", ContentText: "body"}, true}, {"content", KnowledgeDocumentRequest{SourceType: "note", Title: "title"}, true}} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.request.Input()
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestPresentKnowledge(t *testing.T) {
	response := PresentKnowledge("q", []usecase.KnowledgeSnippet{{ChunkID: 1, DocumentID: 2, Text: "fact", Score: 30}})
	if response.Query != "q" || len(response.Items) != 1 || response.Items[0].Snippet != "fact" {
		t.Fatalf("response=%#v", response)
	}
}
