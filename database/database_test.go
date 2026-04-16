package database

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

func setupTestRepo(t *testing.T) (Repository, sqlmock.Sqlmock, *miniredis.Miniredis) {
	// Mock SQL
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %s", err)
	}

	// Mock Redis
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %s", err)
	}

	rdb := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	repo := &postgresRedisRepository{
		db:  db,
		rdb: rdb,
		ctx: context.Background(),
	}

	return repo, mock, mr
}

func TestGetUserByUsername(t *testing.T) {
	repo, mock, mr := setupTestRepo(t)
	defer repo.Close()
	defer mr.Close()

	username := "testuser"
	expectedUser := &User{ID: 1, Username: username, Password: "hashed_password"}

	t.Run("User exists", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"id", "username", "password"}).
			AddRow(expectedUser.ID, expectedUser.Username, expectedUser.Password)

		mock.ExpectQuery("SELECT id, username, password FROM users WHERE username = \\$1").
			WithArgs(username).
			WillReturnRows(rows)

		user, err := repo.GetUserByUsername(context.Background(), username)
		assert.NoError(t, err)
		assert.NotNil(t, user)
		assert.Equal(t, expectedUser.Username, user.Username)
	})

	t.Run("User not found", func(t *testing.T) {
		mock.ExpectQuery("SELECT id, username, password FROM users WHERE username = \\$1").
			WithArgs("unknown").
			WillReturnError(sql.ErrNoRows)

		user, err := repo.GetUserByUsername(context.Background(), "unknown")
		assert.NoError(t, err)
		assert.Nil(t, user)
	})
}

func TestCreateURL(t *testing.T) {
	repo, mock, mr := setupTestRepo(t)
	defer repo.Close()
	defer mr.Close()

	t.Run("Successful creation", func(t *testing.T) {
		longURL := "https://google.com"
		userID := 1
		expectedID := int64(123)

		mock.ExpectQuery("INSERT INTO urls \\(long_url, user_id\\) VALUES \\(\\$1, \\$2\\) RETURNING id").
			WithArgs(longURL, userID).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(expectedID))

		id, err := repo.CreateURL(context.Background(), longURL, userID)
		assert.NoError(t, err)
		assert.Equal(t, expectedID, id)
	})
}

func TestRedisCaching(t *testing.T) {
	repo, _, mr := setupTestRepo(t)
	defer repo.Close()
	defer mr.Close()

	ctx := context.Background()
	shortCode := "abc123"
	longURL := "https://example.com"

	t.Run("Set and Get Cache", func(t *testing.T) {
		err := repo.SetCachedURL(ctx, shortCode, longURL, time.Minute)
		assert.NoError(t, err)

		// Verify in miniredis directly
		cachedLongURL, err := mr.Get(shortCode)
		assert.NoError(t, err)
		assert.Equal(t, longURL, cachedLongURL)

		cached, err := repo.GetCachedURL(ctx, shortCode)
		assert.NoError(t, err)
		assert.Equal(t, longURL, cached)
	})
}
