# Vinculum

"The [vinculum is the] processing device at the core of every Borg vessel.
It interconnects the minds of all the drones."
   -- Seven of Nine (In Voyager episode "Infinite Regress")

Vinculum is several things:

- **WebSocket Protocol** - A simple, JSON-based protocol with server implementation to expose the bus over WebSockets, enabling real-time web applications.
- **WebSocket Client** - A client implementation of the protocol for connecting Go applications to Vinculum WebSocket servers.

## 🌐 WebSocket Components

Vinculum includes WebSocket client and server implementations for real-time web communication:

### 📡 **WebSocket Server**
Expose your EventBus over WebSockets for real-time web applications:
- **Real-time event streaming** to web clients
- **Bidirectional communication** (subscribe + publish)
- **Flexible authentication** and authorization policies
- **Built-in metrics** and connection management
- **Message transformations** and filtering

📖 **[WebSocket Server Documentation](pkg/vinculum/vws/server/README.md)**

### 🔌 **WebSocket Client**
Connect to Vinculum WebSocket servers from Go applications:
- **Auto-reconnection** with exponential backoff
- **Subscription management** and persistence
- **Thread-safe** operations
- **Comprehensive error handling**
- **Builder pattern** for easy configuration

📖 **[WebSocket Client Documentation](pkg/vinculum/vws/client/README.md)**

## 🚀 Quick Start

For WebSocket usage, see the individual component documentation:


### 📋 **Protocol**
Both components implement the Vinculum WebSocket Protocol:
- **JSON-based** with compact message format
- **MQTT-style** topic patterns
- **Request/response** correlation
- **Error handling** and acknowledgments

📖 **[Protocol Specification](pkg/vinculum/vws/PROTOCOL.md)**

## 🎯 Use Cases

- **Microservice communication** within a process
- **Event-driven architectures** 
- **Decoupled component communication**
- **Real-time data processing pipelines**
- **Plugin systems** with event coordination
- **Application telemetry** and monitoring
- **Real-time web applications** with WebSocket integration

## 📄 License

MIT License - see [LICENSE](LICENSE) file for details.

---

**Vinculum** (Latin: "bond" or "link") - connecting your application components with reliable, observable messaging.
