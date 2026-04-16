package database

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestRepositoryIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	// 1. Spin up PostgreSQL Container
	pgContainer, err := postgres.RunContainer(ctx,
		testcontainers.WithImage("postgres:15-alpine"),
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(5*time.Second)),
	)
	assert.NoError(t, err)
	defer pgContainer.Terminate(ctx)

	pgConnStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	assert.NoError(t, err)

	// 2. Spin up Redis Container
	redisContainer, err := redis.RunContainer(ctx,
		testcontainers.WithImage("redis:7-alpine"),
	)
	assert.NoError(t, err)
	defer redisContainer.Terminate(ctx)

	redisAddr, err := redisContainer.ConnectionString(ctx)
	assert.NoError(t, err)

	// 3. Setup Environment for NewRepository
	os.Setenv("DATABASE_URL", pgConnStr)
	os.Setenv("REDIS_URL", redisAddr)

	repo, err := NewRepository(ctx)
	assert.NoError(t, err)
	defer repo.Close()

	// 4. Run Integrated Test Flow
	t.Run("Full URL Life Cycle", func(t *testing.T) {
		// Create a real user
		username := "integration_user"
		err := repo.CreateUser(ctx, username, "hashed_pass")
		assert.NoError(t, err)

		userID, err := repo.GetUserIDByUsername(ctx, username)
		assert.NoError(t, err)

		// Create a URL
		longURL := "https://example.com/very/long/path"
		id, err := repo.CreateURL(ctx, longURL, userID)
		assert.NoError(t, err)

		shortCode := "intg123"
		err = repo.UpdateURLShortCode(ctx, id, shortCode)
		assert.NoError(t, err)

		// Test Cache Integration
		err = repo.SetCachedURL(ctx, shortCode, longURL, time.Minute)
		assert.NoError(t, err)

		cached, err := repo.GetCachedURL(ctx, shortCode)
		assert.NoError(t, err)
		assert.Equal(t, longURL, cached)

		// Test Hit Counter Integration
		err = repo.IncrementHitCounter(ctx, shortCode)
		assert.NoError(t, err)

		// Verify Hit Sync Flow
		count, err := repo.GetAndResetHitCounter(ctx, "hits:"+shortCode)
		assert.NoError(t, err)
		assert.Equal(t, int64(1), count)

		err = repo.UpdateURLHits(ctx, shortCode, count)
		assert.NoError(t, err)

		// Verify DB persistence
		dbURL, err := repo.GetURLByShortCode(ctx, shortCode)
		assert.NoError(t, err)
		assert.NotNil(t, dbURL)
		assert.Equal(t, 1, dbURL.Hits)
	})
}
