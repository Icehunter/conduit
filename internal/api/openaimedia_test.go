package api

import (
	"encoding/json"
	"testing"
)

func mediaMessage() Message {
	return Message{Role: "user", Content: []ContentBlock{
		{Type: "image", Source: &ImageSource{Type: "base64", MediaType: "image/png", Data: "IMG"}},
		{Type: "document", Source: &ImageSource{Type: "base64", MediaType: "application/pdf", Data: "PDF"}},
		{Type: "text", Text: "what is this?"},
	}}
}

func noVision(model string) (bool, bool) { return false, model == "text-only" }

func TestChatMessages_UserMedia(t *testing.T) {
	tests := []struct {
		name  string
		model string
		cfg   Config
		want  string
	}{
		{"image and pdf become content parts", "m", Config{},
			`{"role":"user","content":[{"type":"text","text":"what is this?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,IMG"}},{"type":"file","file":{"filename":"document.pdf","file_data":"data:application/pdf;base64,PDF"}}]}`},
		{"known text-only model gets a note, not the image", "text-only", Config{ModelVision: noVision},
			`{"role":"user","content":[{"type":"text","text":"what is this?\n\n` + imageOmittedText + `"},{"type":"file","file":{"filename":"document.pdf","file_data":"data:application/pdf;base64,PDF"}}]}`},
		{"unknown model keeps the image", "other", Config{ModelVision: noVision},
			`{"role":"user","content":[{"type":"text","text":"what is this?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,IMG"}},{"type":"file","file":{"filename":"document.pdf","file_data":"data:application/pdf;base64,PDF"}}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := openAIMessagesFromMessage(mediaMessage(), reasoningPolicy{}, tt.cfg, tt.model)
			if len(msgs) != 1 {
				t.Fatalf("messages = %+v", msgs)
			}
			raw, err := json.Marshal(msgs[0])
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != tt.want {
				t.Errorf("got  %s\nwant %s", raw, tt.want)
			}
		})
	}
}

func TestChatMessages_ImageOnlyTextOnlyModelStillSendsNote(t *testing.T) {
	msg := Message{Role: "user", Content: []ContentBlock{{Type: "image", Source: &ImageSource{Type: "base64", MediaType: "image/png", Data: "IMG"}}}}
	msgs := openAIMessagesFromMessage(msg, reasoningPolicy{}, Config{ModelVision: noVision}, "text-only")
	if len(msgs) != 1 || msgs[0].Content != imageOmittedText || msgs[0].Parts != nil {
		t.Fatalf("messages = %+v", msgs)
	}
}

func TestChatMessages_PlainTextStaysAString(t *testing.T) {
	msgs := openAIMessagesFromMessage(Message{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}, reasoningPolicy{}, Config{}, "m")
	raw, _ := json.Marshal(msgs[0])
	if string(raw) != `{"role":"user","content":"hi"}` {
		t.Errorf("got %s", raw)
	}
}

func TestResponsesInputs_UserMedia(t *testing.T) {
	tests := []struct {
		name  string
		model string
		cfg   Config
		want  string
	}{
		{"input_image and input_file", "m", Config{},
			`{"role":"user","content":[{"type":"input_text","text":"what is this?"},{"type":"input_image","image_url":"data:image/png;base64,IMG"},{"type":"input_file","filename":"document.pdf","file_data":"data:application/pdf;base64,PDF"}]}`},
		{"text-only model", "text-only", Config{ModelVision: noVision},
			`{"role":"user","content":[{"type":"input_text","text":"what is this?\n\n` + imageOmittedText + `"},{"type":"input_file","filename":"document.pdf","file_data":"data:application/pdf;base64,PDF"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := openAIResponsesInputsFromMessage(mediaMessage(), tt.cfg, tt.model)
			if len(items) != 1 {
				t.Fatalf("items = %+v", items)
			}
			raw, _ := json.Marshal(items[0])
			if string(raw) != tt.want {
				t.Errorf("got  %s\nwant %s", raw, tt.want)
			}
		})
	}
}
