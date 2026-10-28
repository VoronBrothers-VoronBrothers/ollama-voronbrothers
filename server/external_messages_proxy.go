package server

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// externalMessagesProxyMiddleware intercepts POST /v1/messages requests whose
// model belongs to a configured external provider and forwards the body raw
// (passthrough) to {provider.BaseURL}/messages in Anthropic Messages format.
//
// Unlike /api/chat (which re-encodes messages through our own structs), only
// the "model" field is replaced here, so explicit fields like cache_control
// reach the provider untouched — required for Anthropic-style prompt caching.
func externalMessagesProxyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}

		body, err := readRequestBody(c.Request)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			c.Abort()
			return
		}

		model, ok := extractModelField(body)
		if !ok {
			c.Next()
			return
		}

		extRef, isExternal := tryParseExternalModel(model)
		if !isExternal {
			c.Next()
			return
		}

		outBody, err := replaceJSONModelField(body, extRef.ModelID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "external provider: malformed request body"})
			c.Abort()
			return
		}

		forwardExternalMessages(c, extRef, outBody)
		c.Abort()
	}
}

// forwardExternalMessages streams the provider's Anthropic-format response
// back to the client as-is (status, headers and body).
func forwardExternalMessages(c *gin.Context, extRef *externalModelRef, body []byte) {
	outReq, err := http.NewRequestWithContext(
		c.Request.Context(),
		http.MethodPost,
		extRef.Provider.BaseURL+"/messages",
		bytes.NewReader(body),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "external provider: " + err.Error()})
		return
	}
	outReq.Header.Set("Content-Type", "application/json")
	outReq.Header.Set("Authorization", "Bearer "+extRef.Provider.APIKey)

	resp, err := http.DefaultClient.Do(outReq)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "external provider unreachable: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	copyProxyResponseHeaders(c.Writer.Header(), resp.Header)
	c.Status(resp.StatusCode)

	flusher, canFlush := c.Writer.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := c.Writer.Write(buf[:n]); writeErr != nil {
				slog.Debug("external messages proxy: client disconnected", "error", writeErr)
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
		switch readErr {
		case nil:
		case io.EOF:
			return
		default:
			slog.Warn("external messages proxy: response copy failed", "error", readErr)
			return
		}
	}
}
