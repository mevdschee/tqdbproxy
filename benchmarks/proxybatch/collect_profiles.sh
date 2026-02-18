#!/bin/bash
# Collect pprof profiles from the running proxy server

echo "Collecting profiles from proxy server at :9090..."

# Collect CPU profile (30 seconds)
echo "1. Collecting CPU profile (30s)..."
curl -s "http://localhost:9090/debug/pprof/profile?seconds=30" > proxy_cpu.prof
echo "   -> proxy_cpu.prof"

# Collect heap profile
echo "2. Collecting heap profile..."
curl -s "http://localhost:9090/debug/pprof/heap" > proxy_heap.prof
echo "   -> proxy_heap.prof"

# Collect goroutine profile
echo "3. Collecting goroutine profile..."
curl -s "http://localhost:9090/debug/pprof/goroutine" > proxy_goroutine.prof
echo "   -> proxy_goroutine.prof"

# Collect allocs profile
echo "4. Collecting allocs profile..."
curl -s "http://localhost:9090/debug/pprof/allocs" > proxy_allocs.prof
echo "   -> proxy_allocs.prof"

echo ""
echo "Profiles collected! To analyze:"
echo "  go tool pprof proxy_cpu.prof"
echo "  go tool pprof proxy_heap.prof"
echo "  go tool pprof proxy_goroutine.prof"
echo "  go tool pprof proxy_allocs.prof"
echo ""
echo "Or use web UI:"
echo "  go tool pprof -http=:8080 proxy_cpu.prof"
