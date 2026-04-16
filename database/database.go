package database

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"golang.org/x/crypto/bcrypt"

	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/viper"
)

// User represents a user in the system.
type User struct {
	ID       int
	Username string
	Password string // Hashed password
}

// URL represents a shortened URL.
type URL struct {
	ID        int
	LongURL   string
	ShortCode string
	Hits      int
	UserID    int
}

// Repository defines the interface for database and cache operations.
type Repository interface {
	// User operations
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	CreateUser(ctx context.Context, username, hashedPassword string) error
	GetUserIDByUsername(ctx context.Context, username string) (int, error)

	// URL operations
	CreateURL(ctx context.Context, longURL string, userID int) (int64, error)
	CreateURLWithAlias(ctx context.Context, longURL, alias string, userID int) error
	GetURLByShortCode(ctx context.Context, shortCode string) (*URL, error)
	UpdateURLShortCode(ctx context.Context, id int64, shortCode string) error
	ListURLsByUserID(ctx context.Context, userID int) ([]URL, error)
	DeleteURL(ctx context.Context, shortCode string, userID int) (int64, error)
	UpdateURLHits(ctx context.Context, shortCode string, hits int64) error // For batch update

	// Redis Cache operations
	GetCachedURL(ctx context.Context, shortCode string) (string, error)
	SetCachedURL(ctx context.Context, shortCode, longURL string, expiration time.Duration) error
	IncrementHitCounter(ctx context.Context, shortCode string) error
	DeleteCachedURL(ctx context.Context, shortCode string) error
	DeleteHitCounter(ctx context.Context, shortCode string) error
	GetAllHitCounterKeys(ctx context.Context) ([]string, error)
	GetAndResetHitCounter(ctx context.Context, key string) (int64, error)

	// Utility
	Close()
	PingDB(ctx context.Context) error
	PingRedis(ctx context.Context) error
	GetRedisClient() *redis.Client // Expose Redis client for rate limiter middleware
}

// postgresRedisRepository implements the Repository interface.
type postgresRedisRepository struct {
	db  *sql.DB
	rdb *redis.Client
	ctx context.Context // Base context for operations, can be overridden
}

// NewRepository initializes and returns a new Repository instance.
func NewRepository(ctx context.Context) (Repository, error) {
	// Initialize PostgreSQL
	connStr := viper.GetString("DATABASE_URL")
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Postgres: %w", err)
	}

	// Initialize Redis
	redisAddr := viper.GetString("REDIS_URL")
	rdb := redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})

	repo := &postgresRedisRepository{db: db, rdb: rdb, ctx: ctx}

	// Initialize schema
	if err := repo.initSchema(); err != nil {
		return nil, fmt.Errorf("failed to initialize database schema: %w", err)
	}

	// Insert default admin user for demonstration
	if err := repo.insertDefaultAdminUser(); err != nil {
		log.Println("Warning:", err) // Log warning, don't fail startup
	}

	return repo, nil
}

func (r *postgresRedisRepository) initSchema() error {
	query := `
	CREATE TABLE IF NOT EXISTS users (
		id SERIAL PRIMARY KEY,
		username TEXT UNIQUE NOT NULL,
		password TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS urls (
		id SERIAL PRIMARY KEY,
		long_url TEXT NOT NULL,
		short_code TEXT UNIQUE,
		hits INTEGER DEFAULT 0,
		user_id INTEGER REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE TABLE IF NOT EXISTS url_stats (
		id SERIAL PRIMARY KEY,
		short_code TEXT NOT NULL,
		accessed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		ip_address TEXT,
		user_agent TEXT
	);` // Note: ON DELETE CASCADE for user_id in urls table

	_, err := r.db.Exec(query)
	return err
}

func (r *postgresRedisRepository) insertDefaultAdminUser() error {
	hashedPassword, hashErr := bcrypt.GenerateFromPassword([]byte("password"), bcrypt.DefaultCost)
	if hashErr != nil {
		return fmt.Errorf("failed to hash default password: %w", hashErr)
	}
	_, err := r.db.Exec("INSERT INTO users (username, password) VALUES ($1, $2) ON CONFLICT (username) DO NOTHING", "admin", string(hashedPassword))
	if err != nil {
		return fmt.Errorf("could not insert default admin user (might already exist): %w", err)
	}
	return nil
}

// Close closes the database and Redis connections.
func (r *postgresRedisRepository) Close() {
	if r.db != nil {
		log.Println("Closing Postgres connection...")
		r.db.Close()
	}
	if r.rdb != nil {
		log.Println("Closing Redis connection...")
		r.rdb.Close()
	}
}

// PingDB checks the PostgreSQL connection.
func (r *postgresRedisRepository) PingDB(ctx context.Context) error {
	return r.db.PingContext(ctx)
}

// PingRedis checks the Redis connection.
func (r *postgresRedisRepository) PingRedis(ctx context.Context) error {
	return r.rdb.Ping(ctx).Err()
}

// GetRedisClient returns the underlying Redis client.
func (r *postgresRedisRepository) GetRedisClient() *redis.Client {
	return r.rdb
}

// GetUserByUsername retrieves a user by their username.
func (r *postgresRedisRepository) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	user := &User{}
	err := r.db.QueryRowContext(ctx, "SELECT id, username, password FROM users WHERE username = $1", username).Scan(&user.ID, &user.Username, &user.Password)
	if err == sql.ErrNoRows {
		return nil, nil // User not found
	}
	return user, err
}

// CreateUser creates a new user.
func (r *postgresRedisRepository) CreateUser(ctx context.Context, username, hashedPassword string) error {
	_, err := r.db.ExecContext(ctx, "INSERT INTO users (username, password) VALUES ($1, $2)", username, hashedPassword)
	return err
}

// GetUserIDByUsername retrieves a user's ID by their username.
func (r *postgresRedisRepository) GetUserIDByUsername(ctx context.Context, username string) (int, error) {
	var userID int
	err := r.db.QueryRowContext(ctx, "SELECT id FROM users WHERE username = $1", username).Scan(&userID)
	return userID, err
}

// CreateURL creates a new shortened URL.
func (r *postgresRedisRepository) CreateURL(ctx context.Context, longURL string, userID int) (int64, error) {
	var id int64
	err := r.db.QueryRowContext(ctx, "INSERT INTO urls (long_url, user_id) VALUES ($1, $2) RETURNING id", longURL, userID).Scan(&id)
	return id, err
}

// CreateURLWithAlias creates a new shortened URL with a custom alias.
func (r *postgresRedisRepository) CreateURLWithAlias(ctx context.Context, longURL, alias string, userID int) error {
	_, err := r.db.ExecContext(ctx, "INSERT INTO urls (long_url, short_code, user_id) VALUES ($1, $2, $3)", longURL, alias, userID)
	return err
}

// UpdateURLShortCode updates the short code for a given URL ID.
func (r *postgresRedisRepository) UpdateURLShortCode(ctx context.Context, id int64, shortCode string) error {
	_, err := r.db.ExecContext(ctx, "UPDATE urls SET short_code = $1 WHERE id = $2", shortCode, id)
	return err
}

// GetURLByShortCode retrieves a URL by its short code.
func (r *postgresRedisRepository) GetURLByShortCode(ctx context.Context, shortCode string) (*URL, error) {
	url := &URL{}
	err := r.db.QueryRowContext(ctx, "SELECT id, long_url, short_code, hits, user_id FROM urls WHERE short_code = $1", shortCode).Scan(&url.ID, &url.LongURL, &url.ShortCode, &url.Hits, &url.UserID)
	if err == sql.ErrNoRows {
		return nil, nil // URL not found
	}
	return url, err
}

// ListURLsByUserID lists all URLs created by a specific user.
func (r *postgresRedisRepository) ListURLsByUserID(ctx context.Context, userID int) ([]URL, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT short_code, long_url, hits FROM urls WHERE user_id = $1", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var urls []URL
	for rows.Next() {
		var url URL
		if err := rows.Scan(&url.ShortCode, &url.LongURL, &url.Hits); err != nil {
			return nil, err
		}
		urls = append(urls, url)
	}
	return urls, nil
}

// DeleteURL deletes a URL by its short code and user ID.
func (r *postgresRedisRepository) DeleteURL(ctx context.Context, shortCode string, userID int) (int64, error) {
	result, err := r.db.ExecContext(ctx, "DELETE FROM urls WHERE short_code = $1 AND user_id = $2", shortCode, userID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// UpdateURLHits updates the aggregated hit count for a short code.
func (r *postgresRedisRepository) UpdateURLHits(ctx context.Context, shortCode string, hits int64) error {
	_, err := r.db.ExecContext(ctx, "UPDATE urls SET hits = hits + $1 WHERE short_code = $2", hits, shortCode)
	return err
}

// GetCachedURL retrieves a long URL from Redis cache.
func (r *postgresRedisRepository) GetCachedURL(ctx context.Context, shortCode string) (string, error) {
	return r.rdb.Get(ctx, shortCode).Result()
}

// SetCachedURL sets a long URL in Redis cache with an expiration.
func (r *postgresRedisRepository) SetCachedURL(ctx context.Context, shortCode, longURL string, expiration time.Duration) error {
	return r.rdb.Set(ctx, shortCode, longURL, expiration).Err()
}

// IncrementHitCounter increments the hit counter for a short code in Redis.
func (r *postgresRedisRepository) IncrementHitCounter(ctx context.Context, shortCode string) error {
	return r.rdb.Incr(ctx, "hits:"+shortCode).Err()
}

// DeleteCachedURL deletes a URL from Redis cache.
func (r *postgresRedisRepository) DeleteCachedURL(ctx context.Context, shortCode string) error {
	return r.rdb.Del(ctx, shortCode).Err()
}

// DeleteHitCounter deletes a hit counter from Redis.
func (r *postgresRedisRepository) DeleteHitCounter(ctx context.Context, shortCode string) error {
	return r.rdb.Del(ctx, "hits:"+shortCode).Err()
}

// GetAllHitCounterKeys retrieves all Redis keys for hit counters.
func (r *postgresRedisRepository) GetAllHitCounterKeys(ctx context.Context) ([]string, error) {
	var keys []string
	iter := r.rdb.Scan(ctx, 0, "hits:*", 0).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	return keys, iter.Err()
}

// GetAndResetHitCounter atomically gets the value of a hit counter and resets it to 0.
func (r *postgresRedisRepository) GetAndResetHitCounter(ctx context.Context, key string) (int64, error) {
	return r.rdb.GetSet(ctx, key, 0).Int64()
}
