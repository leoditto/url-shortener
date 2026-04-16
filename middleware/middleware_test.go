package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"url_shortener/auth"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/ulule/limiter/v3"
)

func TestAuthMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("Missing Header", func(t *testing.T) {
		r := gin.New()
		r.Use(AuthMiddleware())
		r.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Contains(t, w.Body.String(), "Authorization header is required")
	})

	t.Run("Invalid Format", func(t *testing.T) {
		r := gin.New()
		r.Use(AuthMiddleware())
		r.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		req.Header.Set("Authorization", "InvalidToken")
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Contains(t, w.Body.String(), "Invalid authorization header format")
	})

	t.Run("Valid Token", func(t *testing.T) {
		token, err := auth.GenerateToken("testuser")
		assert.NoError(t, err)

		r := gin.New()
		r.Use(AuthMiddleware())
		r.GET("/test", func(c *gin.Context) {
			user, _ := c.Get("username")
			c.String(http.StatusOK, user.(string))
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "testuser", w.Body.String())
	})
}

func TestErrorHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("No Error", func(t *testing.T) {
		r := gin.New()
		r.Use(ErrorHandler())
		r.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("With Error", func(t *testing.T) {
		r := gin.New()
		r.Use(ErrorHandler())
		r.GET("/test", func(c *gin.Context) {
			_ = c.AbortWithError(http.StatusBadRequest, errors.New("custom error"))
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "custom error")
		assert.Contains(t, w.Body.String(), `"success":false`)
	})
}

func TestRecovery(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(ErrorHandler())
	r.Use(Recovery())
	r.GET("/panic", func(c *gin.Context) {
		panic("something went wrong")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/panic", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "something went wrong")
}

func TestRateLimiter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr, err := miniredis.Run()
	assert.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	rate := limiter.Rate{Period: 1 * time.Hour, Limit: 1}

	r := gin.New()
	r.Use(RateLimiter(rdb, rate))
	r.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)

	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusTooManyRequests, w2.Code)
}
