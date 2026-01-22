# Generic Proxy Component

The `proxy` component provides a foundational TCP proxy implementation that handles basic connection forwarding.

## Role

While protocol-specific proxies (like MySQL) implement deep packet inspection and logic, the generic proxy is used for:
- **Transparent Forwarding**: Passing traffic between a client and a backend server without modification.
- **PostgreSQL Support**: Currently, PostgreSQL is supported via this generic proxy, providing a transparent gateway until the PostgreSQL-specific wire protocol logic is fully implemented.

## Implementation

It uses `io.Copy` in separate goroutines to bidirectionally pipe data between the client connection and the backend connection, ensuring efficient asynchronous I/O.

[Back to Index](../../README.md)
