package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/DatTruong-tora/product-insight-api/internal/models"
	"github.com/DatTruong-tora/product-insight-api/internal/services"
	"github.com/gin-gonic/gin"
)

func GetProductInfo(c *gin.Context) {
	// Read the UPC code from the query parameter, for example: /api/v1/product?upc=123456
	upcCode := c.Query("upc")
	if upcCode == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing 'upc' query parameter"})
		return
	}

	// Delegate the heavier business logic to the service layer.
	productInfo, err := services.AggregateProductData(upcCode)
	if err != nil {
		// Return HTTP 500 if the pipeline fails.
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Return a successful JSON response (HTTP 200).
	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"data":   productInfo,
	})
}

func GetCompanyProducts(c *gin.Context) {
	company := strings.TrimSpace(c.Query("company"))
	if company == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing 'company' query parameter"})
		return
	}

	limit := 5
	if rawLimit := strings.TrimSpace(c.Query("limit")); rawLimit != "" {
		parsedLimit, err := strconv.Atoi(rawLimit)
		if err != nil || parsedLimit <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid 'limit' query parameter"})
			return
		}
		if parsedLimit > 10 {
			parsedLimit = 10
		}
		limit = parsedLimit
	}

	offset := 0
	if rawOffset := strings.TrimSpace(c.Query("offset")); rawOffset != "" {
		parsedOffset, err := strconv.Atoi(rawOffset)
		if err != nil || parsedOffset < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid 'offset' query parameter"})
			return
		}
		offset = parsedOffset
	}

	productList, err := services.AggregateCompanyProducts(company, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"data":   productList,
	})
}

func SearchProducts(c *gin.Context) {
	query := strings.TrimSpace(c.Query("query"))
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing 'query' query parameter"})
		return
	}

	limit := 5
	if rawLimit := strings.TrimSpace(c.Query("limit")); rawLimit != "" {
		parsedLimit, err := strconv.Atoi(rawLimit)
		if err != nil || parsedLimit <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid 'limit' query parameter"})
			return
		}
		if parsedLimit > 20 {
			parsedLimit = 20
		}
		limit = parsedLimit
	}

	minScore := float32(0.6)
	if rawMinScore := strings.TrimSpace(c.Query("min_score")); rawMinScore != "" {
		parsedMinScore, err := strconv.ParseFloat(rawMinScore, 32)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid 'min_score' query parameter"})
			return
		}
		minScore = float32(parsedMinScore)
	}

	searchResult, err := services.SearchProducts(query, limit, minScore)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"data":   searchResult,
	})
}

func GetRelatedPatents(c *gin.Context) {
	var request models.RelatedPatentsRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON request body"})
		return
	}

	inventionText := strings.TrimSpace(request.InventionText)
	keyPhrases := make([]string, 0, len(request.KeyPhrases))
	for _, p := range request.KeyPhrases {
		p = strings.TrimSpace(p)
		if p != "" {
			keyPhrases = append(keyPhrases, p)
		}
	}

	if inventionText == "" && len(keyPhrases) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Provide non-empty 'invention_text' and/or 'key_phrases'"})
		return
	}

	relatedPatents, err := services.FindRelatedPatentIDs(c.Request.Context(), inventionText, keyPhrases, request.Limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"data":   relatedPatents,
	})
}
