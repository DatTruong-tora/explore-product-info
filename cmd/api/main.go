package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DatTruong-tora/product-insight-api/internal/handlers"
	"github.com/DatTruong-tora/product-insight-api/internal/middleware"
	"github.com/DatTruong-tora/product-insight-api/internal/services"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"golang.org/x/time/rate"
)

// envPatentNoiseCSVPath overrides CSV discovery when set to a non-empty path to an existing file.
const envPatentNoiseCSVPath = "PATENT_NOISE_CSV_PATH"

func main() {
	// Load the .env file containing sensitive values such as GEMINI_API_KEY.
	if err := godotenv.Load(); err != nil {
		log.Println("Warning: .env file not found, falling back to system environment variables")
	}

	noiseCSV, err := resolveNoiseDataCSVPath()
	if err != nil {
		log.Fatalf("resolve noise_data.csv: %v", err)
	}
	if err := services.InitializePatentNoiseCleaner(noiseCSV); err != nil {
		log.Fatalf("patent noise cleaner: %v", err)
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

// resolveNoiseDataCSVPath finds the patent noise CSV.
// If envPatentNoiseCSVPath is set to a non-empty path, that path is used when it exists
// and is a regular file; otherwise an error names the env var. If unset or blank, tries
// noise_data.csv in the working directory, then ../noise_data.csv.
func resolveNoiseDataCSVPath() (string, error) {
	if raw := strings.TrimSpace(os.Getenv(envPatentNoiseCSVPath)); raw != "" {
		st, err := os.Stat(raw)
		if err != nil {
			return "", fmt.Errorf("%s=%q: %w", envPatentNoiseCSVPath, raw, err)
		}
		if st.IsDir() {
			return "", fmt.Errorf("%s=%q: path is a directory, not a file", envPatentNoiseCSVPath, raw)
		}
		abs, err := filepath.Abs(raw)
		if err != nil {
			return raw, nil
		}
		return abs, nil
	}

	candidates := []string{
		"noise_data.csv",
		filepath.Join("..", "noise_data.csv"),
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			abs, err := filepath.Abs(p)
			if err != nil {
				return p, nil
			}
			return abs, nil
		}
	}
	wd, _ := os.Getwd()
	return "", fmt.Errorf("noise_data.csv not found (cwd=%q); tried noise_data.csv and ../noise_data.csv", wd)
}
