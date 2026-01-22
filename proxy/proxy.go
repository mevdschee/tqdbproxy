package proxy

import (
	"io"
	"log"
	"net"
	"sync"
)

// Proxy handles TCP connections between clients and a backend server
type Proxy struct {
	name    string
	listen  string
	backend string
}

// New creates a new proxy instance
func New(name, listen, backend string) *Proxy {
	return &Proxy{
		name:    name,
		listen:  listen,
		backend: backend,
	}
}

// Start begins accepting connections
func (p *Proxy) Start() error {
	listener, err := net.Listen("tcp", p.listen)
	if err != nil {
		return err
	}
	log.Printf("[%s] Listening on %s, forwarding to %s", p.name, p.listen, p.backend)

	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				log.Printf("[%s] Accept error: %v", p.name, err)
				continue
			}
			go p.handleConnection(client)
		}
	}()

	return nil
}

func (p *Proxy) handleConnection(client net.Conn) {
	defer client.Close()

	backend, err := net.Dial("tcp", p.backend)
	if err != nil {
		log.Printf("[%s] Backend connection error: %v", p.name, err)
		return
	}
	defer backend.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	// Client -> Backend
	go func() {
		defer wg.Done()
		io.Copy(backend, client)
	}()

	// Backend -> Client
	go func() {
		defer wg.Done()
		io.Copy(client, backend)
	}()

	wg.Wait()
}
