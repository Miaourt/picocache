# Picocache

A kinda dumb HTTP caching proxy.

Requests to `http://<picocache>/path/to/file` will be proxied to 
`https://<src>/path/to/file` and cached locally, with LRU eviction.

## Configuration

All configuration is done via environment variables:

| Variable | Description | Example |
|----------|-------------|---------|
| `PICOCACHE_SRC` | Source URL to proxy | `https://example.com` |
| `PICOCACHE_DIR` | Cache directory path | `/var/cache/picocache` |
| `PICOCACHE_MAXSIZE` | Maximum cache size | `10GB`, `500MB` |
| `PICOCACHE_LISTENTO` | Listen address | `:8080`, `127.0.0.1:3000` |

## Usage

### Direct

```sh
export PICOCACHE_SRC="https://example.com"
export PICOCACHE_DIR="./cache"
export PICOCACHE_MAXSIZE="1GB"
export PICOCACHE_LISTENTO=":8080"
go run main.go
```

### Docker

```sh
docker build -t picocache .
docker run -p 8080:8080 \
  -e PICOCACHE_SRC="https://example.com" \
  -e PICOCACHE_DIR="/cache" \
  -e PICOCACHE_MAXSIZE="1GB" \
  -e PICOCACHE_LISTENTO=":8080" \
  -v ./cache:/cache \
  picocache
```

### Docker Compose

```yaml
services:
  picocache:
    build: .
    ports:
      - "8080:8080"
    environment:
      PICOCACHE_SRC: "https://example.com"
      PICOCACHE_DIR: "/cache"
      PICOCACHE_MAXSIZE: "1GB"
      PICOCACHE_LISTENTO: ":8080"
    volumes:
      - ./cache:/cache
```