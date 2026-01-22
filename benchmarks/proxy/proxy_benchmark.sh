#!/bin/bash
set -e

# TQDBProxy Overhead Benchmark
# Measures proxy overhead at different query complexities

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

# Cleanup function
cleanup() {
    echo "Stopping proxy..."
    pkill -9 tqdbproxy 2>/dev/null || true
    rm -f benchmark-tool
}
trap cleanup EXIT

# Check if proxy binary exists
if [ ! -f "../../tqdbproxy" ]; then
    echo "Building tqdbproxy..."
    cd ../..
    go build -o tqdbproxy ./cmd/tqdbproxy
    cd "$SCRIPT_DIR"
fi

# Build benchmark tool
echo "Building benchmark tool..."
go build -o benchmark-tool .

# Start proxy
echo "Starting tqdbproxy..."
pkill -9 tqdbproxy 2>/dev/null || true
sleep 1

# Get project root
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Run proxy from project root (where config.ini is)
cd "$PROJECT_ROOT"
./tqdbproxy > /dev/null 2>&1 &
PROXY_PID=$!
cd "$SCRIPT_DIR"
sleep 3

# Verify proxy is running
if ! kill -0 $PROXY_PID 2>/dev/null; then
    echo "Failed to start proxy"
    exit 1
fi

# Run benchmark
echo ""
echo "Running benchmark..."
echo ""
./benchmark-tool -n 200 -csv > proxy_benchmark.csv

# Display results
echo ""
./benchmark-tool -n 200

# Generate visualization
echo ""
echo "Generating visualization..."

python3 << 'EOF'
import pandas as pd
import matplotlib.pyplot as plt
import numpy as np

# Load data
df = pd.read_csv('proxy_benchmark.csv')

# Create figure with single chart
fig, ax = plt.subplots(figsize=(12, 6))

# Bar positions
x = np.arange(len(df))
width = 0.35

# Two bars: Direct MySQL vs Proxy
bars1 = ax.bar(x - width/2, df['DirectRPS'], width, label='Direct MySQL', color='#2ecc71')
bars2 = ax.bar(x + width/2, df['ProxyRPS'], width, label='TQDBProxy', color='#3498db')

ax.set_xlabel('Query Complexity', fontsize=12)
ax.set_ylabel('Requests Per Second (RPS)', fontsize=12)
ax.set_title('TQDBProxy Performance: Direct MySQL vs Proxy\nHigher is better', fontsize=14)
ax.set_xticks(x)
ax.set_xticklabels(df['QueryType'])
ax.legend(loc='upper right', fontsize=11)
ax.grid(axis='y', linestyle='--', alpha=0.7)

# Add value labels on bars
def add_labels(bars):
    for bar in bars:
        height = bar.get_height()
        if height >= 1000:
            label = f'{height/1000:.1f}k'
        else:
            label = f'{height:.0f}'
        ax.annotate(label,
                    xy=(bar.get_x() + bar.get_width() / 2, height),
                    xytext=(0, 3), textcoords="offset points",
                    ha='center', va='bottom', fontsize=10, fontweight='bold')

add_labels(bars1)
add_labels(bars2)

# Increase y limit for label space
ax.set_ylim(0, max(df['DirectRPS'].max(), df['ProxyRPS'].max()) * 1.15)

plt.tight_layout()
plt.savefig('proxy_benchmark.png', dpi=150, bbox_inches='tight')
print("Saved: proxy_benchmark.png")
EOF

echo ""
echo "============================================="
echo "Benchmark completed!"
echo "Generated files:"
echo "  - proxy_benchmark.csv"
echo "  - proxy_benchmark.png"
echo "============================================="
