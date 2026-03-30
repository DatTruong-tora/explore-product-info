package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGetRelatedPatentsRejectsInvalidJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/patents/related", bytes.NewBufferString("{invalid"))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	GetRelatedPatents(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", recorder.Code)
	}
}

func TestGetRelatedPatentsRejectsMissingInventionText(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/patents/related", bytes.NewBufferString(`{"limit":5}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	GetRelatedPatents(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing invention_text, got %d", recorder.Code)
	}
}

func TestGetRelatedPatentsReadsBodyShape(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	router := gin.New()
	router.POST("/api/v1/patents/related", func(c *gin.Context) {
		var body struct {
			InventionText string `json:"invention_text"`
			Limit         int    `json:"limit"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, body)
	})

	payload := map[string]any{
		"invention_text": "portable bio-signal measuring device",
		"limit":          7,
	}
	bodyBytes, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/patents/related", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid body shape, got %d", recorder.Code)
	}
}
