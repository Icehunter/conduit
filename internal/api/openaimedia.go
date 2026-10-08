package api

import (
	"encoding/json"
	"strings"
)

// imageOmittedText replaces an image block sent to a model known to lack
// vision, so the turn still says something was attached.
const imageOmittedText = "[image omitted: the selected model does not accept image input]"

// mediaDataURL is the data: URL for a base64 image or document source.
func mediaDataURL(src *ImageSource) (string, bool) {
	if src == nil || src.Data == "" || (src.Type != "" && src.Type != "base64") {
		return "", false
	}
	mt := src.MediaType
	if mt == "" {
		mt = "application/octet-stream"
	}
	return "data:" + mt + ";base64," + src.Data, true
}

// visionBlocked reports whether model is known, through Config.ModelVision,
// to lack image input. Unknown models get the image.
func (c Config) visionBlocked(model string) bool {
	if c.ModelVision == nil {
		return false
	}
	vision, known := c.ModelVision(model)
	return known && !vision
}

// openAIChatPart is one element of a chat message's content array.
type openAIChatPart struct {
	Type     string             `json:"type"`
	Text     string             `json:"text,omitempty"`
	ImageURL *openAIChatImage   `json:"image_url,omitempty"`
	File     *openAIChatFileRef `json:"file,omitempty"`
}

type openAIChatImage struct {
	URL string `json:"url"`
}

type openAIChatFileRef struct {
	Filename string `json:"filename"`
	FileData string `json:"file_data"`
}

// MarshalJSON sends Parts as the content array when set, otherwise Content as
// a plain string.
func (m openAIChatMessage) MarshalJSON() ([]byte, error) {
	type plain openAIChatMessage // drops this method to avoid recursion
	if len(m.Parts) == 0 {
		return json.Marshal(plain(m))
	}
	return json.Marshal(struct {
		plain
		Content []openAIChatPart `json:"content"`
	}{plain: plain(m), Content: m.Parts})
}

// chatMediaParts returns the content array for a user message that carries
// images or documents, or nil when it has none (the caller then sends a plain
// string), and the text to send: text, plus imageOmittedText when an image
// was dropped for a text-only model. Text comes first as one part, then the
// media in order.
func chatMediaParts(blocks []ContentBlock, text string, cfg Config, model string) ([]openAIChatPart, string) {
	var media []openAIChatPart
	omitted := false
	for _, b := range blocks {
		switch b.Type {
		case "image":
			if cfg.visionBlocked(model) {
				omitted = true
				continue
			}
			if url, ok := mediaDataURL(b.Source); ok {
				media = append(media, openAIChatPart{Type: "image_url", ImageURL: &openAIChatImage{URL: url}})
			}
		case "document":
			if url, ok := mediaDataURL(b.Source); ok {
				media = append(media, openAIChatPart{Type: "file", File: &openAIChatFileRef{Filename: documentFilename(b.Source), FileData: url}})
			}
		}
	}
	if omitted {
		text = strings.TrimSpace(text + "\n\n" + imageOmittedText)
	}
	if len(media) == 0 {
		return nil, text
	}
	var parts []openAIChatPart
	if text != "" {
		parts = append(parts, openAIChatPart{Type: "text", Text: text})
	}
	return append(parts, media...), text
}

// documentFilename names an inline document; the wire requires one.
func documentFilename(src *ImageSource) string {
	if src != nil && src.MediaType == "application/pdf" {
		return "document.pdf"
	}
	return "document"
}

// responsesMediaContent returns the Responses content parts for a user
// message's images and documents, and the text to send with them (which
// carries imageOmittedText when an image was dropped for a text-only model).
func responsesMediaContent(blocks []ContentBlock, text string, cfg Config, model string) (parts []openAIResponsesContent, outText string) {
	outText = text
	for _, b := range blocks {
		switch b.Type {
		case "image":
			if cfg.visionBlocked(model) {
				outText = strings.TrimSpace(outText + "\n\n" + imageOmittedText)
				continue
			}
			if url, ok := mediaDataURL(b.Source); ok {
				parts = append(parts, openAIResponsesContent{Type: "input_image", ImageURL: url})
			}
		case "document":
			if url, ok := mediaDataURL(b.Source); ok {
				parts = append(parts, openAIResponsesContent{Type: "input_file", Filename: documentFilename(b.Source), FileData: url})
			}
		}
	}
	return parts, outText
}
