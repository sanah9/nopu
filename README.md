# Nopu

## Project Overview

Nopu is a completely **free and open source** push service based on the Nostr protocol


## System Architecture

```
External Nostr Relay → Event Listener → Redis Queue → Event Processor → Subscription Server → Subscription Client
                                                                
```

## Quick Start

### 1. Environment Setup

```bash
# Install Redis
# Ubuntu/Debian
sudo apt-get install redis-server

# macOS
brew install redis

# Start Redis
redis-server
```

### 2. Configuration

```bash
# Copy configuration file
cp config.yaml.example config.yaml

# Or use environment variables
export REDIS_ADDR="localhost:6379"
export RELAY_PORT="8080"
export DOMAIN="localhost:8080"
export LISTEN_RELAYS="wss://relay.damus.io,wss://nos.lol"
export LISTEN_KINDS="1,7"
```

### 3. Start Service

```bash
# Install dependencies 
make deps

# Run service
make run

# Or run directly
go run cmd/main.go
```

