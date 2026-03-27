package handlers

import (
	"net/http"
	"strconv"
	"strings"

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
