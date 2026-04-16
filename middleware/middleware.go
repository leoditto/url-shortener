package middleware

import (
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"url_shortener/auth"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"github.com/ulule/limiter/v3"
	mgin "github.com/ulule/limiter/v3/drivers/middleware/gin"
	sredis "github.com/ulule/limiter/v3/drivers/store/redis"
)

// RateLimiter creates a middleware for rate limiting using Redis as a store.
func RateLimiter(rdb *redis.Client, rate limiter.Rate) gin.HandlerFunc {
	store, err := sredis.NewStoreWithOptions(rdb, limiter.StoreOptions{
		Prefix: "rate_limiter:",
	})
	if err != nil {
		panic(fmt.Sprintf("Failed to initialize rate limiter store: %v", err))
	}

	return mgin.NewMiddleware(limiter.New(store, rate), mgin.WithLimitReachedHandler(func(c *gin.Context) {
		log.Printf("[Rate Limit] Warning: Limit reached for IP: %s | Path: %s", c.ClientIP(), c.Request.URL.Path)
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
			"success": false,
			"message": "Rate limit exceeded. Please try again later.",
		})
	}))
}

// AuthMiddleware validates JWT tokens and sets the username in the context.
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header is required"})
			c.Abort()
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization header format"})
			c.Abort()
			return
		}

		tokenString := parts[1]
		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return auth.GetSecretKey(), nil
		})

		if err != nil || !token.Valid {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			c.Abort()
			return
		}

		if claims, ok := token.Claims.(jwt.MapClaims); ok {
			c.Set("username", claims["username"])
		}

		c.Next()
	}
}

// ErrorHandler catches errors attached to the context and formats them into a standard JSON structure.
func ErrorHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		// Check if there are any errors attached to the context
		if len(c.Errors) > 0 {
			err := c.Errors.Last()
			status := c.Writer.Status()
			if status < 400 {
				status = http.StatusInternalServerError
			}

			log.Printf("[API Error] %s %s | Status: %d | Error: %v",
				c.Request.Method,
				c.Request.URL.Path,
				status,
				err.Error(),
			)

			c.AbortWithStatusJSON(status, gin.H{
				"success": false,
				"message": err.Error(),
			})
		}
	}
}

// Recovery catches panics, logs them with a stack trace, and delegates error formatting to ErrorHandler.
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				// Log the stack trace for server-side debugging
				log.Printf("[Panic Recovered] %v\n%s", err, debug.Stack())

				// Attach the panic as an error so ErrorHandler can pick it up
				c.Error(fmt.Errorf("internal server error: %v", err))
				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()
		c.Next()
	}
}
