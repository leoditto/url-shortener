# Go URL Shortener

A high-performance URL shortener built with Go, featuring PostgreSQL for persistent storage and Redis as a high-speed caching and rate-limiting layer.

## Features

- **URL Shortening**: Generate short codes for long URLs or provide custom aliases.
- **High Performance**: Redis caching for redirects and asynchronous hit tracking.
- **Security**: JWT-based authentication for protected endpoints and bcrypt password hashing.
- **Rate Limiting**: Distributed rate limiting using Redis to prevent abuse.
- **Statistics**: Track hit counts for every shortened URL.
- **Dockerized**: Ready for deployment with Docker and Docker Compose.

## Prerequisites

- Docker and Docker Compose
- Go 1.25 (for local development)

## Getting Started

1. **Clone the repository**
2. **Run the application using Docker Compose**:
   ```bash
   docker-compose up --build
   ```
   The server will start on `http://localhost:8080`.

---

## API Documentation

### 1. Public Endpoints

#### Health Check
Verify the connectivity of the API, Database, and Redis.
```bash
curl -X GET http://localhost:8080/health
```

#### User Registration
Create a new account.
```bash
curl -X POST http://localhost:8080/register \
     -H "Content-Type: application/json" \
     -d '{"username": "dev_user", "password": "secure_password"}'
```

#### User Login
Exchange credentials for a JWT token.
```bash
curl -X POST http://localhost:8080/login \
     -H "Content-Type: application/json" \
     -d '{"username": "dev_user", "password": "secure_password"}'
```
*Response contains a `"token"` which must be used in the `Authorization: Bearer <token>` header for protected routes.*

#### URL Redirect
Redirect to the original long URL.
```bash
curl -I http://localhost:8080/{short_code}
```

---

### 2. Protected Endpoints
*Require `Authorization: Bearer <JWT_TOKEN>`*

#### Shorten URL
Create a short link. Providing an `alias` is optional.
```bash
curl -X POST http://localhost:8080/shorten \
     -H "Authorization: Bearer <YOUR_TOKEN>" \
     -H "Content-Type: application/json" \
     -d '{"url": "https://www.google.com", "alias": "google"}'
```

#### Get URL Statistics
Get hit counts and metadata for a specific code.
```bash
curl -X GET http://localhost:8080/stats/google \
     -H "Authorization: Bearer <YOUR_TOKEN>"
```

#### List My URLs
List all URLs created by the authenticated user.
```bash
curl -X GET http://localhost:8080/my-urls \
     -H "Authorization: Bearer <YOUR_TOKEN>"
```

#### Delete URL
Remove a short URL owned by the user.
```bash
curl -X DELETE http://localhost:8080/url/google \
     -H "Authorization: Bearer <YOUR_TOKEN>"
```

---

## Testing

The project includes both unit tests and integration tests. Integration tests use Testcontainers to spin up real instances of PostgreSQL and Redis automatically.

### Running Tests Locally
Ensure you have a Docker daemon running on your machine:
```bash
go test -v ./...
```

### Running Tests via Docker Compose
To run the entire test suite in a clean, isolated environment (recommended for CI):
```bash
docker-compose -f docker-compose.test.yml up --build --abort-on-container-exit
```

---

## Project Structure

```text
.
├── auth/               # JWT token generation logic
├── database/           # Repository pattern and data access logic
├── middleware/         # Auth and Rate Limiting middlewares
├── main.go             # Application entry point and routing
├── Dockerfile          # Multi-stage build for production
├── docker-compose.yml  # Orchestration for App, Postgres, and Redis
└── go.mod              # Dependency management
```

## License
MIT