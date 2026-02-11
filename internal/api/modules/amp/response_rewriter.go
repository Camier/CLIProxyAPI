package amp

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ResponseRewriter wraps a gin.ResponseWriter to intercept and modify the response body
// It's used to rewrite model names in responses when model mapping is used
type ResponseRewriter struct {
	gin.ResponseWriter
	body          *bytes.Buffer
	originalModel string
	isStreaming   bool
}

var (
	jsonModelFieldNeedle   = []byte(`"model`)
	jsonContentFieldNeedle = []byte(`"content"`)
)

func shouldBufferForJSONRewrite(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if ct == "" {
		return true
	}

	return strings.Contains(ct, "json") || strings.Contains(ct, "javascript")
}

func mayNeedRewrite(data []byte, originalModel string) bool {
	if bytes.Contains(data, jsonContentFieldNeedle) {
		return true
	}
	if originalModel == "" {
		return false
	}

	return bytes.Contains(data, jsonModelFieldNeedle)
}

// NewResponseRewriter creates a new response rewriter for model name substitution
func NewResponseRewriter(w gin.ResponseWriter, originalModel string) *ResponseRewriter {
	return &ResponseRewriter{
		ResponseWriter: w,
		body:           &bytes.Buffer{},
		originalModel:  originalModel,
	}
}

// Write intercepts response writes and buffers them for model name replacement
func (rw *ResponseRewriter) Write(data []byte) (int, error) {
	// Detect streaming on first write
	if rw.body.Len() == 0 && !rw.isStreaming {
		contentType := rw.Header().Get("Content-Type")
		rw.isStreaming = strings.Contains(contentType, "text/event-stream") ||
			strings.Contains(contentType, "stream")
	}

	if rw.isStreaming {
		n, err := rw.ResponseWriter.Write(rw.rewriteStreamChunk(data))
		if err == nil {
			if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		return n, err
	}

	if !shouldBufferForJSONRewrite(rw.Header().Get("Content-Type")) {
		return rw.ResponseWriter.Write(data)
	}

	return rw.body.Write(data)
}

// Flush writes the buffered response with model names rewritten
func (rw *ResponseRewriter) Flush() {
	if rw.isStreaming {
		if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}
	if rw.body.Len() > 0 {
		payload := rw.body.Bytes()
		if mayNeedRewrite(payload, rw.originalModel) {
			payload = rw.rewriteModelInResponse(payload)
		}
		if _, err := rw.ResponseWriter.Write(payload); err != nil {
			log.Warnf("amp response rewriter: failed to write rewritten response: %v", err)
		}
	}
}

// modelFieldPaths lists all JSON paths where model name may appear
var modelFieldPaths = []string{"message.model", "model", "modelVersion", "response.model", "response.modelVersion"}

// rewriteModelInResponse replaces all occurrences of the mapped model with the original model in JSON
// It also suppresses "thinking" blocks if "tool_use" is present to ensure Amp client compatibility
func (rw *ResponseRewriter) rewriteModelInResponse(data []byte) []byte {
	if !mayNeedRewrite(data, rw.originalModel) {
		return data
	}

	// 1. Amp Compatibility: Suppress thinking blocks if tool use is detected
	// The Amp client struggles when both thinking and tool_use blocks are present
	if bytes.Contains(data, jsonContentFieldNeedle) && gjson.GetBytes(data, `content.#(type=="tool_use")`).Exists() {
		filtered := gjson.GetBytes(data, `content.#(type!="thinking")#`)
		if filtered.Exists() {
			originalCount := gjson.GetBytes(data, "content.#").Int()
			filteredCount := filtered.Get("#").Int()

			if originalCount > filteredCount {
				var err error
				data, err = sjson.SetBytes(data, "content", filtered.Value())
				if err != nil {
					log.Warnf("Amp ResponseRewriter: failed to suppress thinking blocks: %v", err)
				} else {
					if log.IsLevelEnabled(log.DebugLevel) {
						log.Debugf("Amp ResponseRewriter: Suppressed %d thinking blocks due to tool usage", originalCount-filteredCount)
						log.Debugf("Amp ResponseRewriter: Resulting content: %s", gjson.GetBytes(data, "content").String())
					}
				}
			}
		}
	}

	if rw.originalModel == "" || !bytes.Contains(data, jsonModelFieldNeedle) {
		return data
	}
	for _, path := range modelFieldPaths {
		if gjson.GetBytes(data, path).Exists() {
			data, _ = sjson.SetBytes(data, path, rw.originalModel)
		}
	}
	return data
}

// rewriteStreamChunk rewrites model names in SSE stream chunks
func (rw *ResponseRewriter) rewriteStreamChunk(chunk []byte) []byte {
	if rw.originalModel == "" {
		return chunk
	}

	// SSE format: "data: {json}\n\n"
	lines := bytes.Split(chunk, []byte("\n"))
	for i, line := range lines {
		if bytes.HasPrefix(line, []byte("data: ")) {
			jsonData := bytes.TrimPrefix(line, []byte("data: "))
			if len(jsonData) > 0 && jsonData[0] == '{' {
				if !mayNeedRewrite(jsonData, rw.originalModel) {
					continue
				}
				// Rewrite JSON in the data line
				rewritten := rw.rewriteModelInResponse(jsonData)
				lines[i] = append([]byte("data: "), rewritten...)
			}
		}
	}

	return bytes.Join(lines, []byte("\n"))
}
