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

