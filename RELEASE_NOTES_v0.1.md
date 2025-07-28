# Nopu v0.1.0 Release Notes

## 🎉 First Public Release

Nopu is a completely **free and open source** push service based on the Nostr protocol. This is our first public release with core functionality implemented.

## ✨ What's New in v0.1.0

### Core Features
- **Subscription Server**: Self-hostable server that subscribes to relay events and receives client subscriptions
- **Push Server**: Handles message forwarding via APNs (Apple Push Notification service)
- **NIP-29 Integration**: Built-in subscription management leveraging the NIP-29 relay implementation
- **Event Flow**: Complete event processing pipeline from client subscription to push delivery

### Technical Highlights
- **Memory-based Queue**: Replaced Redis dependency with in-memory queue for simplified deployment
- **APNs Support**: Full Apple Push Notification service integration
- **Online Presence Detection**: Real-time client online status tracking
- **Event Wrapping**: Kind 20284 events for relay event wrapping and Kind 20285 for external event injection

### Architecture
- **Modular Design**: Separate subscription and push servers for scalability
- **Self-hostable**: Users can deploy their own push server instead of using nopu.sh
- **Client Freedom**: Clients can choose between private push servers or the public service

## 🚀 Quick Start

### Prerequisites
- Go 1.24.1 or later
- No external dependencies required (Redis removed in favor of in-memory queue)

### Installation
```bash
# Clone the repository
git clone https://github.com/sanah9/nopu.git
cd nopu

# Install dependencies
make deps

# Configure the service
cp config.yaml.example config.yaml
# Edit config.yaml with your settings

# Build and run
make build
make run-both
```

### Configuration
- Copy `config.yaml.example` to `config.yaml`
- Configure your relay endpoints and APNs credentials
- Set up your subscription and push server ports

## 📋 Event Flow

1. **Client Subscription**: Client creates subscription via `kind 9007` event
2. **Event Listening**: Server listens to configured relays and kinds
3. **Filter Matching**: Events are matched against registered group filters
4. **Message Delivery**: 
   - Online clients receive `kind 20284` events (wrapped from relay events)
   - Offline clients receive push notifications via APNs
   - External systems can inject events via `kind 20285` for processing

### Event Types

#### Kind 20284 - Online Message Delivery
The `kind 20284` event wraps the original event as a JSON string and targets a specific NIP-29 group for online clients.

#### Kind 20285 - External Event Injection
The `kind 20285` event is used for external event injection, containing a complete original event in its content that gets processed and matched against client subscriptions.

## 🔧 Development Status

### ✅ Completed Features
- Relay/kinds events listening
- Basic subscription workflow
- Client online presence detection
- APNs push notifications
- Memory-based queue system
- Modular server architecture
- Kind 20284 event wrapping from relay events
- Kind 20285 external event injection with pubkey whitelist

### 🚧 In Progress
- FCM (Firebase Cloud Messaging) support
- Intelligent minimal filters merging
- Performance optimization
- Enhanced error handling

## 📦 Build Artifacts

This release includes:
- `subscription-server`: Subscription management service
- `push-server`: Push notification service
- `relay29/`: NIP-29 relay implementation
- Configuration templates and examples
- Comprehensive test suite

## 🧪 Testing

```bash
# Run all tests
make test-all

# Run specific service tests
make test-subscription
make test-push

# Run integration tests
make test-integration
```

## 🤝 Contributing

We welcome contributions! Please see our contributing guidelines for more information.

## 📄 License

This project is licensed under the MIT License - see the LICENSE file for details.