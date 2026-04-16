package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"url_shortener/auth"
	"url_shortener/database"
	"url_shortener/middleware"

	"github.com/redis/go-redis/v9"
	"github.com/spf13/viper"
	"github.com/ulule/limiter/v3"
	"golang.org/x/crypto/bcrypt"

	"github.com/gin-gonic/gin"
)

const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func encode(n int64) string {
	if n == 0 {
		return string(charset[0])
	}
	res := ""
	for n > 0 {
		res = string(charset[n%62]) + res
		n /= 62
	}
	return res
}

func syncStats(repo database.Repository) {
	// Find all hit keys in Redis
	keys, err := repo.GetAllHitCounterKeys(context.Background())
	if err != nil {
		log.Printf("Failed to get hit counter keys: %v", err)
		return
	}

	for _, key := range keys {
		shortCode := strings.TrimPrefix(key, "hits:")

		// Get and Reset the counter atomically using GETSET
		count, err := repo.GetAndResetHitCounter(context.Background(), key)
		if err != nil {
			log.Printf("Failed to sync hits for %s: %v", shortCode, err)
			continue
		}
		if count == 0 {
			continue
		}

		if err := repo.UpdateURLHits(context.Background(), shortCode, count); err != nil {
			log.Printf("Failed to update hits for %s in DB: %v", shortCode, err)
		}
	}
}

func startStatsTicker(repo database.Repository) {
	ticker := time.NewTicker(1 * time.Minute)
	for range ticker.C {
		syncStats(repo)
	}
}

func initConfig() {
	viper.SetDefault("PORT", "8080")
	viper.SetDefault("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")
	viper.SetDefault("REDIS_URL", "localhost:6379")
	viper.SetDefault("BASE_URL", "http://localhost:8080")
	viper.SetDefault("JWT_SECRET", "secret-key-change-me")
	viper.SetDefault("RATE_LIMIT_PERIOD", "1m")
	viper.SetDefault("RATE_LIMIT_PUBLIC_LIMIT", 100)
	viper.SetDefault("RATE_LIMIT_PRIVATE_LIMIT", 5)
	viper.SetDefault("RATE_LIMIT_AUTH_LIMIT", 10)

	viper.AutomaticEnv()

	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	_ = viper.ReadInConfig()
}

func main() {
	initConfig()

	repo, err := database.NewRepository(context.Background())
	if err != nil {
		log.Fatalf("Failed to initialize repository: %v", err)
	}

	r := gin.Default()

	// Global Error Handling Middleware
	r.Use(middleware.ErrorHandler()) // Outer middleware to format the error
	r.Use(middleware.Recovery())     // Inner middleware to catch the panic

	// Start background synchronization
	go startStatsTicker(repo)

	// Define different rates
	ratePeriod := viper.GetDuration("RATE_LIMIT_PERIOD")
	publicRate := limiter.Rate{
		Period: ratePeriod,
		Limit:  viper.GetInt64("RATE_LIMIT_PUBLIC_LIMIT"),
	}
	privateRate := limiter.Rate{
		Period: ratePeriod,
		Limit:  viper.GetInt64("RATE_LIMIT_PRIVATE_LIMIT"),
	}
	authRate := limiter.Rate{
		Period: ratePeriod,
		Limit:  viper.GetInt64("RATE_LIMIT_AUTH_LIMIT"),
	}

	// Initialize middlewares
	publicMW := middleware.RateLimiter(repo.GetRedisClient(), publicRate)
	privateMW := middleware.RateLimiter(repo.GetRedisClient(), privateRate)
	authMW := middleware.RateLimiter(repo.GetRedisClient(), authRate)

	r.GET("/health", publicMW, func(c *gin.Context) {
		if err := repo.PingDB(c.Request.Context()); err != nil {
			c.AbortWithError(http.StatusServiceUnavailable, errors.New("unhealthy: database down"))
			return
		}

		if err := repo.PingRedis(c.Request.Context()); err != nil {
			c.AbortWithError(http.StatusServiceUnavailable, errors.New("unhealthy: redis down"))
			return
		}

		c.JSON(http.StatusOK, gin.H{"status": "healthy"})
	})

	r.POST("/login", authMW, func(c *gin.Context) {
		var login struct {
			Username string `json:"username" binding:"required"`
			Password string `json:"password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&login); err != nil {
			c.AbortWithError(http.StatusBadRequest, errors.New("Username and password required"))
			return
		}

		user, err := repo.GetUserByUsername(c.Request.Context(), login.Username)
		if err != nil {
			c.AbortWithError(http.StatusInternalServerError, err)
			return
		}
		if user == nil {
			c.AbortWithError(http.StatusUnauthorized, errors.New("Invalid credentials"))
			return
		}

		if err = bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(login.Password)); err != nil {
			c.AbortWithError(http.StatusUnauthorized, errors.New("Invalid credentials"))
			return
		}

		token, err := auth.GenerateToken(login.Username)
		if err != nil {
			c.AbortWithError(http.StatusInternalServerError, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"token": token})
	})

	r.POST("/register", authMW, func(c *gin.Context) {
		var input struct {
			Username string `json:"username" binding:"required"`
			Password string `json:"password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.AbortWithError(http.StatusBadRequest, errors.New("Username and password required"))
			return
		}

		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		if err != nil {
			c.AbortWithError(http.StatusInternalServerError, err)
			return
		}

		err = repo.CreateUser(c.Request.Context(), input.Username, string(hashedPassword))
		if err != nil {
			if strings.Contains(err.Error(), "unique constraint") {
				c.AbortWithError(http.StatusConflict, errors.New("Username already exists"))
			} else {
				c.AbortWithError(http.StatusInternalServerError, err)
			}
			return
		}

		c.JSON(http.StatusCreated, gin.H{"message": "User registered successfully"})
	})

	r.GET("/:code", publicMW, func(c *gin.Context) {
		code := c.Param("code")

		if err := repo.IncrementHitCounter(c.Request.Context(), code); err != nil {
			log.Printf("Failed to increment hit counter for %s: %v", code, err)
		}

		val, err := repo.GetCachedURL(c.Request.Context(), code)
		if err == nil {
			c.Redirect(http.StatusMovedPermanently, val)
			return
		}
		if err != nil && err != redis.Nil { // Handle actual Redis errors, not just key not found (redis.Nil is from go-redis/v8)
			log.Printf("Redis error getting cached URL for %s: %v", code, err)
			// Fall through to DB lookup
		}

		urlData, err := repo.GetURLByShortCode(c.Request.Context(), code)
		if err != nil {
			c.AbortWithError(http.StatusInternalServerError, err)
			return
		}
		if urlData == nil {
			c.AbortWithError(http.StatusNotFound, errors.New("URL not found"))
			return
		}

		repo.SetCachedURL(c.Request.Context(), code, urlData.LongURL, 24*time.Hour)
		c.Redirect(http.StatusMovedPermanently, urlData.LongURL)
	})

	protected := r.Group("/")
	protected.Use(middleware.AuthMiddleware(), privateMW)
	{
		protected.POST("/shorten", func(c *gin.Context) {
			var input struct {
				URL   string `json:"url" binding:"required"`
				Alias string `json:"alias"`
			}
			if err := c.ShouldBindJSON(&input); err != nil {
				c.AbortWithError(http.StatusBadRequest, errors.New("Invalid input"))
				return
			}

			username, _ := c.Get("username")
			userID, err := repo.GetUserIDByUsername(c.Request.Context(), username.(string))
			if err != nil {
				c.AbortWithError(http.StatusInternalServerError, err)
				return
			}

			var shortCode string
			if input.Alias != "" {
				err := repo.CreateURLWithAlias(c.Request.Context(), input.URL, input.Alias, userID)
				if err != nil {
					if strings.Contains(err.Error(), "unique constraint") {
						c.AbortWithError(http.StatusConflict, errors.New("Alias already taken"))
					} else {
						c.AbortWithError(http.StatusInternalServerError, err)
					}
					return
				}
				shortCode = input.Alias
			} else {
				var id int64
				id, err = repo.CreateURL(c.Request.Context(), input.URL, userID)
				if err != nil {
					c.AbortWithError(http.StatusInternalServerError, err)
					return
				}

				shortCode = encode(id)
				err = repo.UpdateURLShortCode(c.Request.Context(), id, shortCode)
				if err != nil {
					c.AbortWithError(http.StatusInternalServerError, err)
					return
				}
			}

			repo.SetCachedURL(c.Request.Context(), shortCode, input.URL, 24*time.Hour)
			c.JSON(http.StatusOK, gin.H{"short_url": fmt.Sprintf("%s/%s", viper.GetString("BASE_URL"), shortCode)})
		})

		protected.GET("/stats/:code", func(c *gin.Context) {
			code := c.Param("code")

			urlData, err := repo.GetURLByShortCode(c.Request.Context(), code)
			if err != nil {
				c.AbortWithError(http.StatusInternalServerError, err)
				return
			}
			if urlData == nil {
				c.AbortWithError(http.StatusNotFound, errors.New("Short URL not found"))
				return
			}

			c.JSON(http.StatusOK, gin.H{
				"short_code": urlData.ShortCode,
				"long_url":   urlData.LongURL,
				"hits":       urlData.Hits,
			})
		})

		protected.GET("/my-urls", func(c *gin.Context) {
			username, _ := c.Get("username")
			userID, err := repo.GetUserIDByUsername(c.Request.Context(), username.(string))
			if err != nil {
				c.AbortWithError(http.StatusInternalServerError, err)
				return
			}

			urls, err := repo.ListURLsByUserID(c.Request.Context(), userID)
			if err != nil {
				c.AbortWithError(http.StatusInternalServerError, err)
				return
			}

			var responseURLs []gin.H
			for _, url := range urls {
				responseURLs = append(responseURLs, gin.H{
					"short_code": url.ShortCode,
					"long_url":   url.LongURL,
					"hits":       url.Hits,
				})
			}
			c.JSON(http.StatusOK, responseURLs)
		})

		protected.DELETE("/url/:code", func(c *gin.Context) {
			code := c.Param("code")
			username, _ := c.Get("username")

			userID, err := repo.GetUserIDByUsername(c.Request.Context(), username.(string))
			if err != nil {
				c.AbortWithError(http.StatusInternalServerError, err)
				return
			}

			rowsAffected, err := repo.DeleteURL(c.Request.Context(), code, userID)
			if err != nil {
				c.AbortWithError(http.StatusInternalServerError, err)
				return
			}

			if rowsAffected == 0 {
				c.AbortWithError(http.StatusNotFound, errors.New("URL not found or unauthorized"))
				return
			}

			// Cleanup Redis cache and hit counter
			repo.DeleteCachedURL(c.Request.Context(), code)
			repo.DeleteHitCounter(c.Request.Context(), code)

			c.JSON(http.StatusOK, gin.H{"message": "URL deleted successfully"})
		})
	}

	srv := &http.Server{
		Addr:    ":" + viper.GetString("PORT"),
		Handler: r,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %s\n", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}

	repo.Close()
	log.Println("Server exiting")
}
