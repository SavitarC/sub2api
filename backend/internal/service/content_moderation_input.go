package service

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"

	"github.com/tidwall/gjson"
)

func ExtractContentModerationText(protocol string, body []byte) string {
	return ExtractContentModerationInput(protocol, body).Text
}

func ExtractContentModerationInput(protocol string, body []byte) ContentModerationInput {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ContentModerationInput{}
	}
	var parts []string
	var images []string
	switch protocol {
	case ContentModerationProtocolAnthropicMessages:
		collectLastAnthropicUserMessage(gjson.GetBytes(body, "messages"), &parts, &images)
	case ContentModerationProtocolOpenAIChat:
		collectLastRoleMessage(gjson.GetBytes(body, "messages"), "user", &parts, &images)
	case ContentModerationProtocolOpenAIResponses:
		collectLastResponsesInput(gjson.GetBytes(body, "input"), &parts, &images)
	case ContentModerationProtocolGemini:
		collectLastGeminiContent(gjson.GetBytes(body, "contents"), &parts, &images)
	case ContentModerationProtocolOpenAIImages:
		addModerationText(&parts, gjson.GetBytes(body, "prompt").String())
		collectContentValue(gjson.GetBytes(body, "images"), &parts, &images)
	default:
		collectLastResponsesInput(gjson.GetBytes(body, "input"), &parts, &images)
		collectLastRoleMessage(gjson.GetBytes(body, "messages"), "user", &parts, &images)
		collectLastGeminiContent(gjson.GetBytes(body, "contents"), &parts, &images)
	}
	out := ContentModerationInput{
		Text:   normalizeContentModerationText(strings.Join(parts, "\n")),
		Images: normalizeModerationImages(images),
	}
	out.Normalize()
	return out
}

func collectLastRoleMessage(messages gjson.Result, role string, parts *[]string, images *[]string) {
	if !messages.IsArray() {
		return
	}
	array := messages.Array()
	if len(array) == 0 {
		return
	}
	last := array[len(array)-1]
	if strings.ToLower(strings.TrimSpace(last.Get("role").String())) != role {
		return
	}
	var candidate []string
	var candidateImages []string
	collectContentValue(last.Get("content"), &candidate, &candidateImages)
	if normalizeContentModerationText(strings.Join(candidate, "\n")) == "" && len(candidateImages) == 0 {
		return
	}
	*parts = append(*parts, candidate...)
	*images = append(*images, candidateImages...)
}

func collectLastAnthropicUserMessage(messages gjson.Result, parts *[]string, images *[]string) {
	if !messages.IsArray() {
		return
	}
	array := messages.Array()
	if len(array) == 0 {
		return
	}
	last := array[len(array)-1]
	if strings.ToLower(strings.TrimSpace(last.Get("role").String())) != "user" {
		return
	}
	var candidate []string
	var candidateImages []string
	collectAnthropicUserContentValue(last.Get("content"), &candidate, &candidateImages)
	if normalizeContentModerationText(strings.Join(candidate, "\n")) == "" && len(candidateImages) == 0 {
		return
	}
	*parts = append(*parts, candidate...)
	*images = append(*images, candidateImages...)
}

func collectAnthropicUserContentValue(value gjson.Result, parts *[]string, images *[]string) {
	switch {
	case !value.Exists():
		return
	case value.Type == gjson.String:
		if !isAnthropicSystemReminderText(value.String()) {
			addModerationText(parts, value.String())
		}
	case value.IsArray():
		value.ForEach(func(_, item gjson.Result) bool {
			collectAnthropicUserContentValue(item, parts, images)
			return true
		})
	case value.IsObject():
		typ := strings.ToLower(strings.TrimSpace(value.Get("type").String()))
		switch typ {
		case "", "text", "input_text", "message":
			if value.Get("text").Exists() && !isAnthropicSystemReminderText(value.Get("text").String()) {
				addModerationText(parts, value.Get("text").String())
			}
			if value.Get("content").Exists() {
				collectAnthropicUserContentValue(value.Get("content"), parts, images)
			}
		case "image_url", "input_image", "image":
			collectContentValue(value, parts, images)
		}
	}
}

func isAnthropicSystemReminderText(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "<system-reminder>")
}

func collectLastResponsesInput(input gjson.Result, parts *[]string, images *[]string) {
	switch {
	case !input.Exists():
		return
	case input.Type == gjson.String:
		addModerationText(parts, input.String())
	case input.IsArray():
		array := input.Array()
		if len(array) == 0 {
			return
		}
		lastIndex := len(array) - 1
		compactionRequest := array[lastIndex].Get("type").String() == "compaction_trigger"
		if compactionRequest {
			for lastIndex--; lastIndex >= 0; lastIndex-- {
				if isResponsesUserTextItem(array[lastIndex]) {
					break
				}
			}
		}
		if lastIndex >= 0 {
			last := array[lastIndex]
			if isResponsesUserTextItem(last) {
				collectContentValue(last.Get("content"), parts, images)
				if last.Get("type").String() == "input_text" || last.Get("text").Exists() {
					collectContentValue(last, parts, images)
				}
			}
		}
		toolOutputEnd := len(array)
		if compactionRequest {
			toolOutputEnd--
		}
		for _, item := range array[:toolOutputEnd] {
			if isResponsesToolOutputType(item.Get("type").String()) {
				collectResponsesToolOutput(item.Get("output"), parts, images)
			}
		}
	case input.IsObject():
		if isResponsesUserTextItem(input) {
			collectContentValue(input.Get("content"), parts, images)
			if input.Get("type").String() == "input_text" || input.Get("text").Exists() {
				collectContentValue(input, parts, images)
			}
		}
		if isResponsesToolOutputType(input.Get("type").String()) {
			collectResponsesToolOutput(input.Get("output"), parts, images)
		}
	}
}

func isResponsesToolOutputType(itemType string) bool {
	switch strings.ToLower(strings.TrimSpace(itemType)) {
	case "function_call_output", "custom_tool_call_output", "mcp_tool_call_output", "tool_search_output":
		return true
	default:
		return false
	}
}

func collectResponsesToolOutput(output gjson.Result, parts *[]string, images *[]string) {
	partsBefore := len(*parts)
	imagesBefore := len(*images)
	seen := make(map[string]struct{})
	collectResponsesToolOutputValue(output, parts, images, seen)
	if len(*parts) != partsBefore || len(*images) != imagesBefore || !output.Exists() {
		return
	}
	if raw := strings.TrimSpace(output.Raw); raw != "" && raw != "null" {
		addModerationText(parts, raw)
	}
}

// collectResponsesToolOutputValue walks every textual leaf in an arbitrary
// structured tool result. Unlike message content, tool output has no fixed
// schema: recognized text/content fields do not make sibling fields safe to
// ignore because the complete object is forwarded back to the model.
func collectResponsesToolOutputValue(value gjson.Result, parts *[]string, images *[]string, seen map[string]struct{}) {
	switch {
	case !value.Exists():
		return
	case value.Type == gjson.String:
		text := strings.TrimSpace(value.String())
		if text == "" || isToolOutputMediaDataURI(text) {
			return
		}
		if _, duplicate := seen[text]; duplicate {
			return
		}
		partsBefore := len(*parts)
		addModerationText(parts, text)
		if len(*parts) != partsBefore {
			seen[text] = struct{}{}
		}
	case value.IsArray():
		value.ForEach(func(_, item gjson.Result) bool {
			collectResponsesToolOutputValue(item, parts, images, seen)
			return true
		})
	case value.IsObject():
		collectContentObjectImages(value, images)
		value.ForEach(func(key, child gjson.Result) bool {
			if key.String() == "type" && isToolOutputContentDiscriminator(child.String()) {
				return true
			}
			collectResponsesToolOutputValue(child, parts, images, seen)
			return true
		})
	}
}

func isToolOutputContentDiscriminator(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "text", "input_text", "output_text", "message", "image_url", "input_image", "image":
		return true
	default:
		return false
	}
}

func isToolOutputMediaDataURI(value string) bool {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "data:image/") || strings.HasPrefix(lower, "data:video/")
}

func isResponsesUserTextItem(item gjson.Result) bool {
	role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
	if role == "user" {
		return responseItemHasModerationText(item)
	}
	if role != "" {
		return false
	}
	return responseItemHasModerationText(item)
}

func responseItemHasModerationText(item gjson.Result) bool {
	var parts []string
	var images []string
	collectContentValue(item.Get("content"), &parts, &images)
	if item.Get("type").String() == "input_text" || item.Get("text").Exists() {
		collectContentValue(item, &parts, &images)
	}
	return normalizeContentModerationText(strings.Join(parts, "\n")) != "" || len(images) > 0
}

func collectLastGeminiContent(contents gjson.Result, parts *[]string, images *[]string) {
	if !contents.IsArray() {
		return
	}
	array := contents.Array()
	if len(array) == 0 {
		return
	}
	last := array[len(array)-1]
	role := strings.ToLower(strings.TrimSpace(last.Get("role").String()))
	if role != "" && role != "user" {
		return
	}
	var candidate []string
	var candidateImages []string
	if arr := last.Get("parts"); arr.IsArray() {
		arr.ForEach(func(_, part gjson.Result) bool {
			addModerationText(&candidate, part.Get("text").String())
			addGeminiModerationImage(&candidateImages, part)
			return true
		})
	}
	if normalizeContentModerationText(strings.Join(candidate, "\n")) == "" && len(candidateImages) == 0 {
		return
	}
	*parts = append(*parts, candidate...)
	*images = append(*images, candidateImages...)
}

func collectContentValue(value gjson.Result, parts *[]string, images *[]string) {
	switch {
	case !value.Exists():
		return
	case value.Type == gjson.String:
		addModerationText(parts, value.String())
	case value.IsArray():
		value.ForEach(func(_, item gjson.Result) bool {
			collectContentValue(item, parts, images)
			return true
		})
	case value.IsObject():
		typ := strings.ToLower(strings.TrimSpace(value.Get("type").String()))
		collectContentObjectImages(value, images)
		switch typ {
		case "", "text", "input_text", "message":
			if value.Get("text").Exists() {
				addModerationText(parts, value.Get("text").String())
			}
			if value.Get("content").Exists() {
				collectContentValue(value.Get("content"), parts, images)
			}
		case "image_url", "input_image", "image":
		}
	}
}

func collectContentObjectImages(value gjson.Result, images *[]string) {
	addModerationImage(images, value.Get("image_url.url").String())
	addModerationImage(images, value.Get("image_url").String())
	addModerationImage(images, value.Get("url").String())
	addModerationImageData(images, value.Get("source.media_type").String(), value.Get("source.data").String())
	addModerationImageData(images, value.Get("source.mediaType").String(), value.Get("source.data").String())
	addModerationImageData(images, value.Get("media_type").String(), value.Get("data").String())
	addModerationImageData(images, value.Get("mime_type").String(), value.Get("data").String())
	addModerationImageData(images, value.Get("mimeType").String(), value.Get("data").String())
	addModerationImage(images, value.Get("source.data").String())
	addModerationImage(images, value.Get("data").String())
	addModerationImage(images, value.Get("base64").String())
}

func addGeminiModerationImage(images *[]string, part gjson.Result) {
	if inlineData := part.Get("inline_data"); inlineData.IsObject() {
		mimeType := strings.TrimSpace(inlineData.Get("mime_type").String())
		data := strings.TrimSpace(inlineData.Get("data").String())
		if mimeType != "" && data != "" {
			addModerationImage(images, fmt.Sprintf("data:%s;base64,%s", mimeType, data))
		}
	}
	if inlineData := part.Get("inlineData"); inlineData.IsObject() {
		mimeType := strings.TrimSpace(inlineData.Get("mimeType").String())
		data := strings.TrimSpace(inlineData.Get("data").String())
		if mimeType != "" && data != "" {
			addModerationImage(images, fmt.Sprintf("data:%s;base64,%s", mimeType, data))
		}
	}
	addModerationImage(images, part.Get("file_data.file_uri").String())
	addModerationImage(images, part.Get("fileData.fileUri").String())
}

func addModerationImageData(images *[]string, mimeType string, data string) {
	mimeType = strings.TrimSpace(mimeType)
	data = strings.TrimSpace(data)
	if mimeType == "" || data == "" {
		return
	}
	addModerationImage(images, fmt.Sprintf("data:%s;base64,%s", mimeType, data))
}

func addModerationImage(images *[]string, image string) {
	image = strings.TrimSpace(image)
	if image == "" {
		return
	}
	if strings.HasPrefix(image, "data:") || strings.HasPrefix(image, "http://") || strings.HasPrefix(image, "https://") {
		*images = append(*images, image)
	}
}

func normalizeModerationImages(images []string) []string {
	out := make([]string, 0, len(images))
	seen := make(map[string]struct{}, len(images))
	for _, image := range images {
		image = strings.TrimSpace(image)
		if image == "" {
			continue
		}
		if _, ok := seen[image]; ok {
			continue
		}
		seen[image] = struct{}{}
		out = append(out, image)
	}
	return out
}

func limitContentModerationImages(images []string) []string {
	if len(images) <= maxContentModerationInputImages {
		return images
	}
	idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(images))))
	if err != nil {
		return images[:maxContentModerationInputImages]
	}
	return []string{images[int(idx.Int64())]}
}

func addModerationText(parts *[]string, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if strings.Contains(text, "<system-reminder>") {
		return
	}
	*parts = append(*parts, text)
}

func normalizeContentModerationText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}
