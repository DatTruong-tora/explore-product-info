package main

import (
	"log"
	"time"

	"github.com/DatTruong-tora/product-insight-api/internal/handlers"
	"github.com/DatTruong-tora/product-insight-api/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"golang.org/x/time/rate"
)

func main() {
	// Load the .env file containing sensitive values such as GEMINI_API_KEY.
	err := godotenv.Load()
	if err != nil {
		log.Println("Warning: .env file not found, falling back to system environment variables")
	}

	// Initialize the default Gin router, including Logger and Recovery middleware.
	router := gin.Default()
	router.Use(middleware.NewRateLimiter(rate.Limit(5), 10, 3*time.Minute).Middleware())

	// Define versioned API route groups.
	v1 := router.Group("/api/v1")
	{
		// Map the HTTP GET request to its handler.
		v1.GET("/product", handlers.GetProductInfo)
		v1.GET("/products-by-company", handlers.GetCompanyProducts)
		v1.POST("/patents/related", handlers.GetRelatedPatents)
		v1.GET("/search", handlers.SearchProducts)
	}

	log.Println("Server is running at http://localhost:8080")
	// Start the server on port 8080.
	if err := router.Run(":8080"); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
