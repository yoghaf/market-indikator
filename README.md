# BTCUSDT Orderflow Data Collector

## Objective
High-frequency orderflow data logger for quantitative research. Captures Market Depth, Trade flow, Open Interest, and computed metrics (CVD, Delta, Score) for statistical edge analysis.

## Architecture
- **Backend (Go)**: Connects to Binance Futures WebSocket, computes metrics, and logs to daily CSVs.
- **Analysis (Python)**: Scripts to validate predictive edge on collected data.
- **Frontend (React)**: Optional visualization (web/).

## Folder Structure
```
.
├── cmd/orderflow/       # Main Go entry point
├── internal/            # Core logic (engine, ingest, logger)
├── logs/                # Daily CSV files (YYYY-MM-DD.csv)
├── web/                 # React frontend
├── edge_check.py        # Python analysis script
├── go.mod               # Go dependencies
└── README.md            # This file
```

## How to Run (Linux VPS)

### 1. Build the Binary
```bash
go build -o orderflow ./cmd/orderflow
```

### 2. Run the Collector
Run in background (e.g., screen/tmux or systemd):
```bash
./orderflow
```
Logs are automatically written to `logs/YYYY-MM-DD.csv`.

### 3. Analyze Data
Run the python script on a specific log file:
```bash
python edge_check.py logs/2026-02-18.csv
```
Dependencies: `pip install -r requirements.txt`

## Configuration
No config file needed. Parameters (pairs, thresholds) are hardcoded in `cmd/orderflow` and `internal/ingest` for reproducibility.
