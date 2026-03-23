package handlers

import (
	"net/http"
	"github.com/gin-gonic/gin"
	"github.com/DatTruong-tora/product-insight-api/internal/services"
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