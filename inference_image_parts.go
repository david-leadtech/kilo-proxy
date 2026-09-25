package main

import "strings"

// Each reference points only to a documented image slot. Never search tool
// arguments, text, local files or arbitrary nested JSON for base64 strings.
type inferenceImagePart struct {
	part map[string]any
	api  string
}

func imageTransportPath(path string) bool {
	return path == "/v1/responses" || path == "/v1/chat/completions" || path == "/v1/messages"
}

func inferenceImageParts(doc map[string]any, path string) []inferenceImagePart {
	var parts []inferenceImagePart
	if !imageTransportPath(path) {
		return parts
	}
	if path == "/v1/responses" {
		for _, part := range responseImageParts(doc) {
			parts = append(parts, inferenceImagePart{part, path})
		}
		return parts
	}
	add := func(part map[string]any) {
		ref := inferenceImagePart{part, path}
		if path == "/v1/chat/completions" && stringValue(part["type"]) != "image_url" {
			return
		}
		if path == "/v1/messages" && (stringValue(part["type"]) != "image" || stringValue(object(part["source"])["type"]) != "base64") {
			return
		}
		if strings.HasPrefix(ref.inline(), "data:") {
			parts = append(parts, ref)
		}
	}
	messages, _ := doc["messages"].([]any)
	for _, value := range messages {
		message := object(value)
		switch stringValue(message["role"]) {
		case "user", "assistant", "system", "developer", "tool":
		default:
			continue
		}
		content, _ := message["content"].([]any)
		for _, value := range content {
			part := object(value)
			add(part)
			if path == "/v1/messages" && stringValue(part["type"]) == "tool_result" {
				results, _ := part["content"].([]any)
				for _, result := range results {
					add(object(result))
				}
			}
		}
	}
	return parts
}

func (p inferenceImagePart) inline() string {
	switch p.api {
	case "/v1/messages":
		source := object(p.part["source"])
		return "data:" + stringValue(source["media_type"]) + ";base64," + stringValue(source["data"])
	case "/v1/chat/completions":
		return stringValue(object(p.part["image_url"])["url"])
	default:
		return stringValue(p.part["image_url"])
	}
}

func (p inferenceImagePart) setURL(value string) {
	switch p.api {
	case "/v1/messages":
		p.part["source"] = map[string]any{"type": "url", "url": value}
	case "/v1/chat/completions":
		object(p.part["image_url"])["url"] = value
	default:
		p.part["image_url"] = value
	}
}

func (p inferenceImagePart) setInline(mime, encoded string) {
	if p.api == "/v1/messages" {
		source := object(p.part["source"])
		source["media_type"], source["data"] = mime, encoded
		return
	}
	p.setURL("data:" + mime + ";base64," + encoded)
}
